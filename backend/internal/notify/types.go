// Package notify alerts via webhook, Slack, Teams or email about the problems
// the panel already detects (k8s/attention.go) and about the loss of
// connection to a cluster, according to configurable rules: the same
// "Needs attention" list, pushed to wherever the team looks. It evaluates
// neither metrics nor expressions: that is Alertmanager.
package notify

import (
	"fmt"
	"strings"
	"time"

	"github.com/bayplan/bayplan/internal/k8s"
)

// ChannelType is a channel's destination.
type ChannelType string

const (
	TypeWebhook ChannelType = "webhook"
	TypeSlack   ChannelType = "slack"
	TypeTeams   ChannelType = "teams"
	TypeEmail   ChannelType = "email"
)

// ChannelConfig is the non-sensitive part of a channel.
type ChannelConfig struct {
	// To: recipients (email only).
	To []string `json:"to,omitempty"`
	// HeaderName: authentication header of the generic webhook (the value
	// goes in the secret). E.g. "Authorization".
	HeaderName string `json:"headerName,omitempty"`
	// InsecureSkipVerify: do not verify the webhook's TLS (lab use only).
	InsecureSkipVerify bool `json:"insecureSkipVerify,omitempty"`
}

// ChannelSecret is what gets encrypted: the URL (in Slack/Teams it IS the
// secret) and the value of the authentication header.
type ChannelSecret struct {
	URL         string `json:"url,omitempty"`
	HeaderValue string `json:"headerValue,omitempty"`
}

// Channel is a notification_channels row. Secret is only filled in when loaded
// for sending (Store.Channel / Store.EnabledChannels).
type Channel struct {
	ID        int64
	Name      string
	Type      ChannelType
	Config    ChannelConfig
	Secret    ChannelSecret
	SecretSet bool
	Enabled   bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Validate checks what each type requires. With requireSecret false (an edit
// that does not touch the URL) only the configuration is checked.
func (c *Channel) Validate(requireSecret bool) error {
	if strings.TrimSpace(c.Name) == "" {
		return fmt.Errorf("name is required")
	}
	switch c.Type {
	case TypeWebhook, TypeSlack, TypeTeams:
		if requireSecret {
			if c.Secret.URL == "" {
				return fmt.Errorf("webhook URL is missing")
			}
			if !strings.HasPrefix(c.Secret.URL, "https://") && !strings.HasPrefix(c.Secret.URL, "http://") {
				return fmt.Errorf("the URL must start with http:// or https://")
			}
		}
		if c.Type != TypeWebhook && c.Config.HeaderName != "" {
			return fmt.Errorf("the custom header only applies to the generic webhook")
		}
	case TypeEmail:
		if len(c.Config.To) == 0 {
			return fmt.Errorf("specify at least one recipient")
		}
		for _, to := range c.Config.To {
			if !strings.Contains(to, "@") {
				return fmt.Errorf("invalid recipient: %q", to)
			}
		}
	default:
		return fmt.Errorf("unknown channel type %q", c.Type)
	}
	return nil
}

// Rule is a notification_rules row.
type Rule struct {
	ID      int64
	Name    string
	Enabled bool
	// ClusterID nil = every cluster.
	ClusterID *int64
	// Filters: empty list = everything.
	Severities []string
	Kinds      []string
	Reasons    []string
	Namespaces []string
	// MinDuration: the problem has to have been active for at least this long
	// before alerting. Without this margin every normal rollout would raise an
	// alert.
	MinDuration time.Duration
	// Cooldown: do not repeat the same problem for the same rule before this.
	Cooldown       time.Duration
	NotifyResolved bool
	ChannelIDs     []int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Validate normalizes and checks the rule.
func (r *Rule) Validate() error {
	if strings.TrimSpace(r.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if len(r.Severities) == 0 {
		r.Severities = []string{string(k8s.SeverityError)}
	}
	for _, s := range r.Severities {
		if s != string(k8s.SeverityError) && s != string(k8s.SeverityWarning) {
			return fmt.Errorf("unknown severity %q", s)
		}
	}
	if r.MinDuration < 0 {
		return fmt.Errorf("minDuration cannot be negative")
	}
	if r.Cooldown < time.Minute {
		return fmt.Errorf("minimum cooldown: 60 s")
	}
	if len(r.ChannelIDs) == 0 {
		return fmt.Errorf("choose at least one channel")
	}
	return nil
}

// Matches says whether a cluster's problem falls under the rule. It is pure
// and looks at neither durations nor cooldowns (that belongs to the engine,
// which holds the state).
func (r *Rule) Matches(clusterID int64, issue k8s.Issue) bool {
	if r.ClusterID != nil && *r.ClusterID != clusterID {
		return false
	}
	if !containsFold(r.Severities, string(issue.Severity)) {
		return false
	}
	if len(r.Kinds) > 0 && !containsFold(r.Kinds, issue.Kind) {
		return false
	}
	if len(r.Reasons) > 0 && !containsFold(r.Reasons, issue.Reason) {
		return false
	}
	if len(r.Namespaces) > 0 && !containsFold(r.Namespaces, issue.Namespace) {
		return false
	}
	return true
}

func containsFold(list []string, v string) bool {
	for _, x := range list {
		if strings.EqualFold(x, v) {
			return true
		}
	}
	return false
}

// Event is the kind of alert.
type Event string

const (
	EventFiring   Event = "firing"
	EventResolved Event = "resolved"
	EventTest     Event = "test"
)

// Message is what gets sent to a channel, already resolved (cluster name, URL
// to the panel). Each sender formats it its own way.
type Message struct {
	Event     Event
	Severity  k8s.Severity
	ClusterID int64
	Cluster   string
	Issue     k8s.Issue
	Rule      string
	// URL to the panel (empty if no public URL is configured).
	URL string
	At  time.Time
}

// Title is the short line every format uses.
func (m Message) Title() string {
	switch {
	case m.Event == EventTest:
		return "Notification test"
	case m.Issue.Kind == KindCluster && m.Event == EventResolved:
		return fmt.Sprintf("Resolved: cluster %s is connected again", m.Cluster)
	case m.Issue.Kind == KindCluster:
		return fmt.Sprintf("Error: cluster %s is not responding", m.Cluster)
	case m.Event == EventResolved:
		return fmt.Sprintf("Resolved: %s %s in %s", m.Issue.Kind, m.Issue.Name, m.Cluster)
	}
	sev := "Warning"
	if m.Severity == k8s.SeverityError {
		sev = "Error"
	}
	return fmt.Sprintf("%s: %s %s · %s in %s", sev, m.Issue.Kind, m.Issue.Name, m.Issue.Reason, m.Cluster)
}

// Text is the plain-text body.
func (m Message) Text() string {
	var b strings.Builder
	if m.Event == EventTest {
		b.WriteString("If you can read this, the channel works.\n")
	} else {
		fmt.Fprintf(&b, "Cluster: %s\n", m.Cluster)
		if m.Issue.Namespace != "" {
			fmt.Fprintf(&b, "Namespace: %s\n", m.Issue.Namespace)
		}
		fmt.Fprintf(&b, "%s: %s\nReason: %s\n", m.Issue.Kind, m.Issue.Name, m.Issue.Reason)
		if m.Issue.Detail != "" {
			fmt.Fprintf(&b, "Detail: %s\n", m.Issue.Detail)
		}
		if !m.Issue.Since.IsZero() {
			fmt.Fprintf(&b, "Since: %s\n", m.Issue.Since.Format(time.RFC3339))
		}
		fmt.Fprintf(&b, "Rule: %s\n", m.Rule)
	}
	if m.URL != "" {
		fmt.Fprintf(&b, "%s\n", m.URL)
	}
	return b.String()
}

// Delivery is a row of the delivery history.
type Delivery struct {
	ID          int64     `json:"id"`
	TS          time.Time `json:"ts"`
	RuleID      *int64    `json:"ruleId,omitempty"`
	RuleName    string    `json:"ruleName"`
	ChannelID   *int64    `json:"channelId,omitempty"`
	ChannelName string    `json:"channelName"`
	Cluster     string    `json:"cluster,omitempty"`
	IssueKey    string    `json:"issueKey,omitempty"`
	Event       Event     `json:"event"`
	Title       string    `json:"title"`
	OK          bool      `json:"ok"`
	Error       string    `json:"error,omitempty"`
}

// ReasonDisconnected is the reason of the synthetic "cluster disconnected"
// problem (Kind "Cluster"); it does not come from attention.go because it is
// not a resource.
const ReasonDisconnected = "Disconnected"

// KindCluster is the Kind of the synthetic connection problem.
const KindCluster = "Cluster"

// KnownReasons are the reasons attention.go emits plus the connection one, so
// that the UI offers a list instead of a free-text field. A reason not listed
// here still works (the rule compares text).
var KnownReasons = []string{
	ReasonDisconnected,
	"CrashLoopBackOff", "ImagePullBackOff", "ErrImagePull", "InvalidImageName",
	"CreateContainerConfigError", "CreateContainerError", "RunContainerError", "CNIConfigError",
	"Pending", "NotReady", "Unknown", "Terminating", "Failed",
	"RolloutIncomplete", "RolloutStalled", "Unavailable", "Paused",
	"Cordoned", "MemoryPressure", "DiskPressure", "PIDPressure", "NetworkUnavailable",
}

// KnownKinds are the kinds that can have problems.
var KnownKinds = []string{"Pod", "Deployment", "StatefulSet", "DaemonSet", "Node", KindCluster}

// IssueKey identifies a problem stably across ticks.
func IssueKey(clusterID int64, i k8s.Issue) string {
	return fmt.Sprintf("%d/%s/%s/%s/%s", clusterID, i.Kind, i.Namespace, i.Name, i.Reason)
}
