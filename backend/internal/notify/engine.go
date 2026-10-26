package notify

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bayplan/bayplan/internal/k8s"
	"github.com/bayplan/bayplan/internal/settings"
)

const (
	tickInterval    = time.Minute
	deliveriesKeep  = 30 * 24 * time.Hour
	deliveriesLimit = 500
)

// issueSource is what the engine needs from the k8s.Manager, as an interface
// so it can be tested with made-up problems.
type issueSource interface {
	ClusterIssues(id int64, now time.Time) (issues []k8s.Issue, connected bool, lastError string)
}

// storeAPI is what the engine uses from the Store, as an interface for tests.
type storeAPI interface {
	Rules(ctx context.Context, onlyEnabled bool) ([]Rule, error)
	EnabledChannels(ctx context.Context) (map[int64]Channel, error)
	RecordDelivery(ctx context.Context, d Delivery) error
	Prune(ctx context.Context, keep time.Duration) error
	LoadState(ctx context.Context) ([]StateRow, error)
	SaveState(ctx context.Context, upserts []StateRow, resolved []string) error
}

// issueState is the memory of a problem across ticks: when it was first seen
// and when each rule was last notified.
type issueState struct {
	issue     k8s.Issue
	clusterID int64
	cluster   string
	firstSeen time.Time
	lastSent  map[int64]time.Time // ruleID -> last send (or the dry record of the first tick)
	// sent: rules that have ACTUALLY been notified. The dry record of the
	// first tick fills lastSent (for the cooldown) but not this: a problem
	// that was already broken before the restart and gets fixed must not
	// produce a "resolved" for something that was never alerted.
	sent     map[int64]bool
	seenTick uint64
	// restored: came from the DB at startup. The startup grace does not apply
	// to these: what was alerted and when is already known.
	restored bool
}

// Engine evaluates the rules every minute against the current problems of
// every enabled cluster and sends whatever is due. The state (what has already
// been alerted) lives in memory and is persisted after every tick in
// notification_state (migration 0024), from where it is reloaded at startup.
// What is recovered from the DB keeps its cooldowns and produces "resolved" if
// it is gone; what was NOT in the DB (problems new to the engine, or a first
// install) goes through the startup grace: the first tick only records it and
// does NOT alert, so a restart does not resend at once everything that was
// already broken.
type Engine struct {
	store    storeAPI
	pool     *pgxpool.Pool
	k8s      issueSource
	settings *settings.Store
	sender   Sender
	logger   *slog.Logger

	// smtpConfig and clusters are injectable for tests.
	smtpConfig func(ctx context.Context) settings.SMTP
	clusters   func(ctx context.Context) (map[int64]string, error)

	mu      sync.Mutex
	state   map[string]*issueState
	tick    uint64
	running bool
}

func NewEngine(store *Store, pool *pgxpool.Pool, k8sManager *k8s.Manager, st *settings.Store, logger *slog.Logger) *Engine {
	e := &Engine{store: store, pool: pool, k8s: k8sManager, settings: st, sender: defaultSender{}, logger: logger, state: map[string]*issueState{}}
	e.smtpConfig = e.loadSMTP
	e.clusters = e.clusterList
	return e
}

// loadSMTP reads the smtp section and decrypts the password.
func (e *Engine) loadSMTP(ctx context.Context) settings.SMTP {
	var cfg settings.SMTP
	if e.settings == nil {
		return cfg
	}
	_ = e.settings.Get(ctx, settings.KeySMTP, &cfg)
	if cfg.Password.IsSet() {
		if pw, err := e.settings.DecryptSecret(cfg.Password); err == nil {
			cfg.SetPassword(pw)
		}
	}
	return cfg
}

func (e *Engine) publicURL(ctx context.Context) string {
	if e.settings == nil {
		return ""
	}
	var srv settings.Server
	_ = e.settings.Get(ctx, settings.KeyServer, &srv)
	return srv.PublicURL
}

// Start launches the loop. Prunes the history at startup.
func (e *Engine) Start(ctx context.Context) {
	if err := e.store.Prune(ctx, deliveriesKeep); err != nil {
		e.logger.Warn("notify: failed pruning history", "error", err)
	}
	if err := e.Restore(ctx); err != nil {
		e.logger.Warn("notify: failed restoring state; starting from scratch with startup grace", "error", err)
	}
	go func() {
		ticker := time.NewTicker(tickInterval)
		defer ticker.Stop()
		e.Tick(ctx, time.Now())
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				e.Tick(ctx, now)
			}
		}
	}()
}

// Restore loads the persisted state. The full Issue is not stored (only the
// key): it is filled in on the first tick if the problem persists, and if it
// does not the "resolved" is built from the key (parseIssueKey).
func (e *Engine) Restore(ctx context.Context) error {
	rows, err := e.store.LoadState(ctx)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, r := range rows {
		st, ok := e.state[r.IssueKey]
		if !ok {
			st = &issueState{issue: parseIssueKey(r.IssueKey), clusterID: r.ClusterID, firstSeen: r.FirstSeen, lastSent: map[int64]time.Time{}, sent: map[int64]bool{}, restored: true}
			e.state[r.IssueKey] = st
		}
		if r.FirstSeen.Before(st.firstSeen) {
			st.firstSeen = r.FirstSeen
		}
		st.lastSent[r.RuleID] = r.LastSent
		st.sent[r.RuleID] = r.Sent
	}
	return nil
}

// parseIssueKey inverts IssueKey so the "resolved" of a problem that
// disappeared while the server was down can be alerted.
func parseIssueKey(key string) k8s.Issue {
	parts := strings.SplitN(key, "/", 5)
	if len(parts) != 5 {
		return k8s.Issue{Name: key}
	}
	return k8s.Issue{Kind: parts[1], Namespace: parts[2], Name: parts[3], Reason: parts[4]}
}

// clusterList returns id -> name of the enabled Kubernetes clusters.
func (e *Engine) clusterList(ctx context.Context) (map[int64]string, error) {
	rows, err := e.pool.Query(ctx, `SELECT id, display_name FROM clusters WHERE enabled AND kind = 'kubernetes' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}

// Tick is one full evaluation. Exported for tests; the loop calls it every
// minute. If a previous tick is still sending, this one is skipped.
func (e *Engine) Tick(ctx context.Context, now time.Time) {
	e.mu.Lock()
	if e.running {
		e.mu.Unlock()
		return
	}
	e.running = true
	e.tick++
	tick := e.tick
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		e.running = false
		e.mu.Unlock()
	}()

	rules, err := e.store.Rules(ctx, true)
	if err != nil {
		e.logger.Error("notify: failed loading rules", "error", err)
		return
	}
	clusters, err := e.clusters(ctx)
	if err != nil {
		e.logger.Error("notify: failed listing clusters", "error", err)
		return
	}

	// 1. Collect the current problems and update the state.
	current := map[string]*issueState{}
	for id, name := range clusters {
		issues, connected, lastError := e.k8s.ClusterIssues(id, now)
		if !connected {
			issues = []k8s.Issue{{Severity: k8s.SeverityError, Kind: KindCluster, Name: name, Reason: ReasonDisconnected, Detail: lastError}}
		}
		for _, is := range issues {
			key := IssueKey(id, is)
			st, ok := e.state[key]
			if !ok {
				since := is.Since
				if since.IsZero() || since.After(now) {
					since = now
				}
				st = &issueState{issue: is, clusterID: id, cluster: name, firstSeen: since, lastSent: map[int64]time.Time{}, sent: map[int64]bool{}}
				e.state[key] = st
			}
			st.issue = is
			st.cluster = name
			st.seenTick = tick
			current[key] = st
		}
	}

	var toSend []pending
	var resolvedKeys []string
	// 2. Resolved: were there and are gone. Only alerted if it had been
	// alerted before.
	for key, st := range e.state {
		if st.seenTick == tick {
			continue
		}
		if st.cluster == "" {
			st.cluster = clusters[st.clusterID]
		}
		for _, r := range rules {
			if st.sent[r.ID] && r.NotifyResolved {
				toSend = append(toSend, pending{rule: r, st: st, event: EventResolved})
			}
		}
		delete(e.state, key)
		resolvedKeys = append(resolvedKeys, key)
	}
	// 3. Active: meet minimum duration and cooldown. On the first tick after
	// startup they are marked as already seen without sending (see Engine).
	for _, st := range current {
		for _, r := range rules {
			if !r.Matches(st.clusterID, st.issue) {
				continue
			}
			if now.Sub(st.firstSeen) < r.MinDuration {
				continue
			}
			if last, ok := st.lastSent[r.ID]; ok && now.Sub(last) < r.Cooldown {
				continue
			}
			if tick == 1 && !st.restored {
				st.lastSent[r.ID] = now
				continue
			}
			toSend = append(toSend, pending{rule: r, st: st, event: EventFiring})
			st.lastSent[r.ID] = now
			st.sent[r.ID] = true
		}
	}
	e.persist(ctx, current, resolvedKeys)
	if len(toSend) == 0 {
		return
	}

	// 4. Send.
	channels, err := e.store.EnabledChannels(ctx)
	if err != nil {
		e.logger.Error("notify: failed loading channels", "error", err)
		return
	}
	smtpCfg := e.smtpConfig(ctx)
	base := e.publicURL(ctx)
	for _, p := range toSend {
		msg := Message{
			Event: p.event, Severity: p.st.issue.Severity, ClusterID: p.st.clusterID, Cluster: p.st.cluster,
			Issue: p.st.issue, Rule: p.rule.Name, At: now,
		}
		if base != "" {
			msg.URL = base + "/clusters/" + itoa(p.st.clusterID)
		}
		for _, chID := range p.rule.ChannelIDs {
			ch, ok := channels[chID]
			if !ok {
				continue // disabled or deleted
			}
			err := redactErr(e.sender.Send(ctx, ch, smtpCfg, msg))
			d := Delivery{
				RuleID: &p.rule.ID, RuleName: p.rule.Name, ChannelID: &ch.ID, ChannelName: ch.Name,
				Cluster: p.st.cluster, IssueKey: IssueKey(p.st.clusterID, p.st.issue), Event: p.event, Title: msg.Title(), OK: err == nil,
			}
			if err != nil {
				d.Error = err.Error()
				e.logger.Warn("notify: failed sending", "channel", ch.Name, "rule", p.rule.Name, "error", err)
			}
			if err := e.store.RecordDelivery(ctx, d); err != nil {
				e.logger.Error("notify: failed recording delivery", "error", err)
			}
		}
	}
}

// persist stores the live state (only the rules with some record: a problem
// that matches no rule takes no row) and deletes what is resolved.
func (e *Engine) persist(ctx context.Context, current map[string]*issueState, resolved []string) {
	var rows []StateRow
	for key, st := range current {
		for ruleID, last := range st.lastSent {
			rows = append(rows, StateRow{IssueKey: key, RuleID: ruleID, ClusterID: st.clusterID, FirstSeen: st.firstSeen, LastSent: last, Sent: st.sent[ruleID]})
		}
	}
	if err := e.store.SaveState(ctx, rows, resolved); err != nil {
		e.logger.Error("notify: failed saving state", "error", err)
	}
}

type pending struct {
	rule  Rule
	st    *issueState
	event Event
}

// Test sends a test message through a channel and records it in the history.
func (e *Engine) Test(ctx context.Context, ch Channel) error {
	msg := Message{Event: EventTest, Severity: k8s.SeverityWarning, Rule: "test", At: time.Now(), URL: e.publicURL(ctx)}
	err := redactErr(e.sender.Send(ctx, ch, e.smtpConfig(ctx), msg))
	d := Delivery{RuleName: "—", ChannelID: &ch.ID, ChannelName: ch.Name, Event: EventTest, Title: msg.Title(), OK: err == nil}
	if err != nil {
		d.Error = err.Error()
	}
	if rerr := e.store.RecordDelivery(ctx, d); rerr != nil {
		e.logger.Error("notify: failed recording test delivery", "error", rerr)
	}
	return err
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
