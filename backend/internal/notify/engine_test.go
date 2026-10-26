package notify

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bayplan/bayplan/internal/k8s"
	"github.com/bayplan/bayplan/internal/settings"
)

type fakeStore struct {
	rules      []Rule
	channels   map[int64]Channel
	deliveries []Delivery
	state      map[string]StateRow // issue_key/rule_id -> row, like the table
}

func (f *fakeStore) LoadState(context.Context) ([]StateRow, error) {
	var out []StateRow
	for _, r := range f.state {
		out = append(out, r)
	}
	return out, nil
}
func (f *fakeStore) SaveState(_ context.Context, upserts []StateRow, resolved []string) error {
	if f.state == nil {
		f.state = map[string]StateRow{}
	}
	for _, r := range upserts {
		f.state[r.IssueKey+"/"+strconv.FormatInt(r.RuleID, 10)] = r
	}
	for _, k := range resolved {
		for sk := range f.state {
			if strings.HasPrefix(sk, k+"/") {
				delete(f.state, sk)
			}
		}
	}
	return nil
}

func (f *fakeStore) Rules(context.Context, bool) ([]Rule, error) { return f.rules, nil }
func (f *fakeStore) EnabledChannels(context.Context) (map[int64]Channel, error) {
	return f.channels, nil
}
func (f *fakeStore) RecordDelivery(_ context.Context, d Delivery) error {
	f.deliveries = append(f.deliveries, d)
	return nil
}
func (f *fakeStore) Prune(context.Context, time.Duration) error { return nil }

type fakeSource struct {
	issues    map[int64][]k8s.Issue
	connected map[int64]bool
}

func (f *fakeSource) ClusterIssues(id int64, _ time.Time) ([]k8s.Issue, bool, string) {
	if c, ok := f.connected[id]; ok && !c {
		return nil, false, "timeout"
	}
	return f.issues[id], true, ""
}

type sent struct {
	channel string
	event   Event
	key     string
}

type fakeSender struct {
	sent []sent
	fail bool
}

func (f *fakeSender) Send(_ context.Context, ch Channel, _ settings.SMTP, m Message) error {
	f.sent = append(f.sent, sent{ch.Name, m.Event, IssueKey(m.ClusterID, m.Issue)})
	if f.fail {
		return context.DeadlineExceeded
	}
	return nil
}

func newTestEngine(store *fakeStore, src *fakeSource, snd *fakeSender) *Engine {
	e := &Engine{store: store, k8s: src, sender: snd, logger: slog.Default(), state: map[string]*issueState{}}
	e.smtpConfig = func(context.Context) settings.SMTP { return settings.SMTP{} }
	e.clusters = func(context.Context) (map[int64]string, error) { return map[int64]string{1: "prod"}, nil }
	return e
}

func crash(t0 time.Time) k8s.Issue {
	return k8s.Issue{Severity: k8s.SeverityError, Kind: "Pod", Namespace: "app", Name: "api-1", Reason: "CrashLoopBackOff", Since: t0}
}

func TestEngineFiresAfterMinDurationAndRespectsCooldown(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	store := &fakeStore{
		rules:    []Rule{{ID: 10, Name: "errors", Enabled: true, Severities: []string{"error"}, MinDuration: 5 * time.Minute, Cooldown: time.Hour, NotifyResolved: true, ChannelIDs: []int64{1}}},
		channels: map[int64]Channel{1: {ID: 1, Name: "slack", Type: TypeSlack}},
	}
	src := &fakeSource{issues: map[int64][]k8s.Issue{1: {crash(t0)}}}
	snd := &fakeSender{}
	e := newTestEngine(store, src, snd)

	// Tick 1 (startup): the problem has been there 10 min, but the first tick
	// never sends: it only records what was already broken.
	e.Tick(context.Background(), t0.Add(10*time.Minute))
	if len(snd.sent) != 0 {
		t.Fatalf("the first tick must not send: %v", snd.sent)
	}
	// Tick 2: within the cooldown of the tick 1 record → nothing.
	e.Tick(context.Background(), t0.Add(11*time.Minute))
	if len(snd.sent) != 0 {
		t.Fatalf("cooldown: %v", snd.sent)
	}
	// New problem younger than MinDuration → not yet.
	src.issues[1] = append(src.issues[1], k8s.Issue{Severity: k8s.SeverityError, Kind: "Deployment", Namespace: "app", Name: "web", Reason: "Unavailable", Since: t0.Add(11 * time.Minute)})
	e.Tick(context.Background(), t0.Add(12*time.Minute))
	if len(snd.sent) != 0 {
		t.Fatalf("minDuration: %v", snd.sent)
	}
	// 5 min into the new problem → sent once.
	e.Tick(context.Background(), t0.Add(17*time.Minute))
	if len(snd.sent) != 1 || snd.sent[0].event != EventFiring || snd.sent[0].key != "1/Deployment/app/web/Unavailable" {
		t.Fatalf("expected 1 firing for the deployment, got %v", snd.sent)
	}
	e.Tick(context.Background(), t0.Add(18*time.Minute))
	if len(snd.sent) != 1 {
		t.Fatalf("must not repeat within the cooldown: %v", snd.sent)
	}
	// The deployment gets fixed → resolved; the pod persists and produces nothing.
	src.issues[1] = []k8s.Issue{crash(t0)}
	e.Tick(context.Background(), t0.Add(19*time.Minute))
	if len(snd.sent) != 2 || snd.sent[1].event != EventResolved {
		t.Fatalf("expected resolved, got %v", snd.sent)
	}
	// After the one-hour cooldown, the pod (recorded "dry" on tick 1) is sent.
	e.Tick(context.Background(), t0.Add(71*time.Minute))
	if len(snd.sent) != 3 || snd.sent[2].key != "1/Pod/app/api-1/CrashLoopBackOff" {
		t.Fatalf("expected firing for the pod after the cooldown, got %v", snd.sent)
	}
	if len(store.deliveries) != 3 || !store.deliveries[0].OK {
		t.Fatalf("history: %+v", store.deliveries)
	}
}

// A problem recorded dry on the first tick that gets fixed does NOT produce a
// "resolved": it was never alerted.
func TestEngineNoResolvedForUnnotifiedIssues(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	store := &fakeStore{
		rules:    []Rule{{ID: 1, Name: "r", Enabled: true, Severities: []string{"error"}, Cooldown: time.Hour, NotifyResolved: true, ChannelIDs: []int64{1}}},
		channels: map[int64]Channel{1: {ID: 1, Name: "hook", Type: TypeWebhook}},
	}
	src := &fakeSource{issues: map[int64][]k8s.Issue{1: {crash(t0)}}}
	snd := &fakeSender{}
	e := newTestEngine(store, src, snd)
	e.Tick(context.Background(), t0)
	src.issues[1] = nil
	e.Tick(context.Background(), t0.Add(time.Minute))
	if len(snd.sent) != 0 {
		t.Fatalf("must not send resolved for something never alerted: %v", snd.sent)
	}
}

func TestEngineDisconnectedClusterIsAnIssue(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	store := &fakeStore{
		rules:    []Rule{{ID: 1, Name: "connection", Enabled: true, Severities: []string{"error"}, Kinds: []string{KindCluster}, Cooldown: time.Hour, ChannelIDs: []int64{1}}},
		channels: map[int64]Channel{1: {ID: 1, Name: "hook", Type: TypeWebhook}},
	}
	src := &fakeSource{connected: map[int64]bool{1: true}}
	snd := &fakeSender{fail: true}
	e := newTestEngine(store, src, snd)
	e.Tick(context.Background(), t0)
	src.connected[1] = false
	e.Tick(context.Background(), t0.Add(time.Minute))
	if len(snd.sent) != 1 || snd.sent[0].key != "1/Cluster//prod/Disconnected" {
		t.Fatalf("got %v", snd.sent)
	}
	if len(store.deliveries) != 1 || store.deliveries[0].OK || store.deliveries[0].Error == "" {
		t.Fatalf("a failed send must be recorded in the history with its error: %+v", store.deliveries)
	}
	// A pod problem does not fall under a Kinds=[Cluster] rule.
	src.connected[1] = true
	src.issues = map[int64][]k8s.Issue{1: {crash(t0)}}
	e.Tick(context.Background(), t0.Add(2*time.Minute))
	// Connection is back: resolved is NOT sent because NotifyResolved=false.
	if len(snd.sent) != 1 {
		t.Fatalf("got %v", snd.sent)
	}
}

func TestRuleMatches(t *testing.T) {
	cid := int64(2)
	r := Rule{ClusterID: &cid, Severities: []string{"error"}, Kinds: []string{"pod"}, Namespaces: []string{"app"}}
	is := crash(time.Now())
	if !r.Matches(2, is) {
		t.Fatal("must match (kind compared case-insensitively)")
	}
	if r.Matches(1, is) {
		t.Fatal("another cluster must not match")
	}
	is.Severity = k8s.SeverityWarning
	if r.Matches(2, is) {
		t.Fatal("warning must not match an error rule")
	}
}

func TestChannelValidate(t *testing.T) {
	if err := (&Channel{Name: "x", Type: TypeEmail}).Validate(true); err == nil {
		t.Fatal("email without recipients must fail")
	}
	if err := (&Channel{Name: "x", Type: TypeSlack, Secret: ChannelSecret{URL: "ftp://x"}}).Validate(true); err == nil {
		t.Fatal("URL without http(s) must fail")
	}
	if err := (&Channel{Name: "x", Type: TypeSlack}).Validate(false); err != nil {
		t.Fatalf("editing without touching the URL must not require it: %v", err)
	}
	if err := rejectLinkLocal("http://169.254.169.254/latest/meta-data"); err == nil {
		t.Fatal("link-local must be rejected")
	}
	if err := rejectLinkLocal("https://hooks.slack.com/services/x"); err != nil {
		t.Fatal(err)
	}
}

func TestRedactErrStripsURL(t *testing.T) {
	err := &url.Error{Op: "Post", URL: "https://hooks.slack.com/services/T000/B000/SECRETTOKEN", Err: errors.New("connection refused")}
	got := redactErr(err).Error()
	if strings.Contains(got, "SECRETTOKEN") || !strings.Contains(got, "connection refused") {
		t.Fatalf("got %q", got)
	}
	if redactErr(nil) != nil {
		t.Fatal("nil must stay nil")
	}
}

// The link-local check also happens at connect time, with the IP already
// resolved: a name resolving to 169.254.x.x does not slip through.
func TestGuardedDialerBlocksLinkLocalAtConnectTime(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := guardedDialer().DialContext(ctx, "tcp", "169.254.169.254:80")
	if !errors.Is(err, errLinkLocal) {
		t.Fatalf("expected errLinkLocal, got %v", err)
	}
}

// TestEngineRestoresStateAcrossRestart: what was alerted before the restart is
// not resent (cooldown recovered) and what got fixed while the server was down
// produces its "resolved".
func TestEngineRestoresStateAcrossRestart(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	rule := Rule{ID: 10, Name: "errors", Enabled: true, Severities: []string{"error"}, MinDuration: 0, Cooldown: time.Hour, NotifyResolved: true, ChannelIDs: []int64{1}}
	store := &fakeStore{rules: []Rule{rule}, channels: map[int64]Channel{1: {ID: 1, Name: "slack", Type: TypeSlack}}}
	gone := k8s.Issue{Severity: k8s.SeverityError, Kind: "Deployment", Namespace: "app", Name: "web", Reason: "RolloutIncomplete", Since: t0}
	src := &fakeSource{issues: map[int64][]k8s.Issue{1: {crash(t0), gone}}}
	snd := &fakeSender{}

	// Instance 1: tick 1 records dry, tick 2 alerts on both.
	e1 := newTestEngine(store, src, snd)
	e1.Tick(context.Background(), t0.Add(time.Minute))
	e1.Tick(context.Background(), t0.Add(2*time.Hour))
	if len(snd.sent) != 2 {
		t.Fatalf("instance 1 should have alerted 2, alerted %d: %+v", len(snd.sent), snd.sent)
	}
	if len(store.state) != 2 {
		t.Fatalf("persisted state: %d rows, expected 2", len(store.state))
	}

	// Restart: the deployment got fixed meanwhile; the pod persists.
	src.issues[1] = []k8s.Issue{crash(t0)}
	snd.sent = nil
	e2 := newTestEngine(store, src, snd)
	if err := e2.Restore(context.Background()); err != nil {
		t.Fatal(err)
	}
	// 10 minutes after the last send: within the cooldown, the crash is not
	// resent; the deployment's resolution is alerted.
	e2.Tick(context.Background(), t0.Add(2*time.Hour+10*time.Minute))
	if len(snd.sent) != 1 || snd.sent[0].event != EventResolved || snd.sent[0].key != IssueKey(1, gone) {
		t.Fatalf("after restarting expected only the deployment's resolved, got %+v", snd.sent)
	}
	if len(store.state) != 1 {
		t.Fatalf("the resolved one should have been deleted from the DB, %d rows remain", len(store.state))
	}
	// Once the cooldown passes, the persisting crash is alerted again.
	snd.sent = nil
	e2.Tick(context.Background(), t0.Add(3*time.Hour+10*time.Minute))
	if len(snd.sent) != 1 || snd.sent[0].event != EventFiring {
		t.Fatalf("expected re-alert after cooldown, got %+v", snd.sent)
	}
}
