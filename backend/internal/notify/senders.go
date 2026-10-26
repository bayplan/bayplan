package notify

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/bayplan/bayplan/internal/netguard"
	"io"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/bayplan/bayplan/internal/k8s"
	"github.com/bayplan/bayplan/internal/settings"
)

// sendTimeout bounds every send: a webhook that does not answer cannot delay
// the other channels nor the next tick.
const sendTimeout = 10 * time.Second

// Sender sends a message through a channel. It is an interface so the engine
// can be tested without network.
type Sender interface {
	Send(ctx context.Context, ch Channel, smtpCfg settings.SMTP, msg Message) error
}

// defaultSender talks to the real destinations.
type defaultSender struct{}

func (defaultSender) Send(ctx context.Context, ch Channel, smtpCfg settings.SMTP, msg Message) error {
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	switch ch.Type {
	case TypeWebhook:
		return postJSON(ctx, ch, webhookPayload(msg))
	case TypeSlack:
		return postJSON(ctx, ch, slackPayload(msg))
	case TypeTeams:
		return postJSON(ctx, ch, teamsPayload(msg))
	case TypeEmail:
		return sendEmail(ctx, smtpCfg, ch.Config.To, msg)
	}
	return fmt.Errorf("unknown channel type %q", ch.Type)
}

// postJSON POSTs to the webhook. The admin sets the URL and it is the
// destination on purpose (SSRF "by design", like any outgoing webhook); the
// only thing blocked is the link-local range, which in every cloud is the
// metadata service holding the instance credentials. Both the URL (a readable
// error when configuring) AND the real IP at connect time are checked (a DNS
// name resolving to 169.254.169.254 or a redirect does not slip through), and
// redirects are not followed: a webhook does not need them.
func postJSON(ctx context.Context, ch Channel, payload any) error {
	if err := rejectLinkLocal(ch.Secret.URL); err != nil {
		return err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ch.Secret.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "bayplan-notify/1")
	if ch.Config.HeaderName != "" && ch.Secret.HeaderValue != "" {
		req.Header.Set(ch.Config.HeaderName, ch.Secret.HeaderValue)
	}
	transport := &http.Transport{DialContext: guardedDialer().DialContext, Proxy: http.ProxyFromEnvironment}
	if ch.Config.InsecureSkipVerify {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // explicit admin option
	}
	client := &http.Client{
		Timeout:   sendTimeout,
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return redactErr(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// redactErr strips the URL from net/http errors: *url.Error includes it
// whole, and in Slack/Teams the URL carries the token. The delivery history
// and the log only need the operation and the cause.
func redactErr(err error) error {
	if err == nil {
		return nil
	}
	var uerr *url.Error
	if errors.As(err, &uerr) {
		return fmt.Errorf("%s: %w", uerr.Op, uerr.Err)
	}
	return err
}

// guardedDialer rejects at connect time any link-local IP, already resolved:
// it is the check that cannot be bypassed with DNS.
func guardedDialer() *net.Dialer {
	return &net.Dialer{
		Timeout: sendTimeout,
		ControlContext: func(_ context.Context, _, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			if ip := net.ParseIP(host); ip != nil && netguard.IsMetadata(ip) {
				return errLinkLocal
			}
			return nil
		},
	}
}

var errLinkLocal = errors.New("the URL points to a link-local address (cloud metadata); not allowed")

func rejectLinkLocal(raw string) error {
	host := raw
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	if i := strings.IndexAny(host, "/?#"); i >= 0 {
		host = host[:i]
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil && (ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()) {
		return errLinkLocal
	}
	return nil
}

// webhookPayload is the generic webhook's JSON: flat, stable fields so that an
// n8n, an Alertmanager-webhook-receiver or a script can consume it without
// parsing text.
func webhookPayload(m Message) map[string]any {
	out := map[string]any{
		"event":     m.Event,
		"severity":  m.Severity,
		"clusterId": m.ClusterID,
		"cluster":   m.Cluster,
		"kind":      m.Issue.Kind,
		"namespace": m.Issue.Namespace,
		"name":      m.Issue.Name,
		"reason":    m.Issue.Reason,
		"detail":    m.Issue.Detail,
		"rule":      m.Rule,
		"title":     m.Title(),
		"text":      m.Text(),
		"url":       m.URL,
		"timestamp": m.At.UTC().Format(time.RFC3339),
	}
	if !m.Issue.Since.IsZero() {
		out["since"] = m.Issue.Since.UTC().Format(time.RFC3339)
	}
	return out
}

func severityEmoji(m Message) string {
	switch {
	case m.Event == EventResolved:
		return "✅"
	case m.Event == EventTest:
		return "🔔"
	case m.Severity == k8s.SeverityError:
		return "🔴"
	}
	return "🟠"
}

// slackPayload: message for an Incoming Webhook (text + one mrkdwn block).
func slackPayload(m Message) map[string]any {
	text := severityEmoji(m) + " *" + m.Title() + "*\n" + strings.ReplaceAll(m.Text(), "\n", "\n")
	if m.URL != "" {
		text = strings.Replace(text, m.URL, "<"+m.URL+"|Open in the panel>", 1)
	}
	return map[string]any{
		"text": m.Title(),
		"blocks": []map[string]any{
			{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": text}},
		},
	}
}

// teamsPayload: Adaptive Card, the format accepted both by the classic
// Incoming Webhooks and by the Power Automate Workflows that replace them.
func teamsPayload(m Message) map[string]any {
	body := []map[string]any{
		{"type": "TextBlock", "size": "Medium", "weight": "Bolder", "text": severityEmoji(m) + " " + m.Title(), "wrap": true},
		{"type": "TextBlock", "text": strings.TrimSpace(strings.Replace(m.Text(), m.URL, "", 1)), "wrap": true},
	}
	card := map[string]any{
		"$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
		"type":    "AdaptiveCard",
		"version": "1.4",
		"body":    body,
	}
	if m.URL != "" {
		card["actions"] = []map[string]any{{"type": "Action.OpenUrl", "title": "Open in the panel", "url": m.URL}}
	}
	return map[string]any{
		"type": "message",
		"attachments": []map[string]any{
			{"contentType": "application/vnd.microsoft.card.adaptive", "content": card},
		},
	}
}

// sendEmail sends the message as plain text over SMTP: implicit TLS on 465,
// STARTTLS if enabled, cleartext otherwise (internal network).
func sendEmail(ctx context.Context, cfg settings.SMTP, to []string, m Message) error {
	if !cfg.Configured() {
		return errors.New("no SMTP server configured (Settings → Outgoing mail)")
	}
	port := cfg.EffectivePort()
	addr := net.JoinHostPort(cfg.Host, fmt.Sprint(port))
	tlsCfg := &tls.Config{ServerName: cfg.Host, InsecureSkipVerify: cfg.InsecureSkipVerify} //nolint:gosec // explicit admin option

	dialer := &net.Dialer{Timeout: sendTimeout}
	var conn net.Conn
	var err error
	if port == 465 {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, tlsCfg)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("SMTP: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(sendTimeout))

	c, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		return fmt.Errorf("SMTP: %w", err)
	}
	defer c.Close()
	if port != 465 && cfg.StartTLS {
		if err := c.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("SMTP STARTTLS: %w", err)
		}
	}
	if cfg.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", cfg.Username, cfg.PlainPassword(), cfg.Host)); err != nil {
			return fmt.Errorf("SMTP auth: %w", err)
		}
	}
	if err := c.Mail(cfg.From); err != nil {
		return fmt.Errorf("SMTP MAIL FROM: %w", err)
	}
	for _, rcpt := range to {
		if err := c.Rcpt(rcpt); err != nil {
			return fmt.Errorf("SMTP RCPT %s: %w", rcpt, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("SMTP DATA: %w", err)
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n",
		cfg.From, strings.Join(to, ", "), sanitizeHeader(m.Title()), m.At.Format(time.RFC1123Z))
	b.WriteString(strings.ReplaceAll(m.Text(), "\n", "\r\n"))
	if _, err := w.Write(b.Bytes()); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("SMTP: %w", err)
	}
	return c.Quit()
}

// sanitizeHeader prevents header injection: a pod name carries no line breaks,
// but the subject is built from text that comes from the cluster.
func sanitizeHeader(s string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
}
