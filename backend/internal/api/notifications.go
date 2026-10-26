package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bayplan/bayplan/internal/audit"
	"github.com/bayplan/bayplan/internal/middleware"
	"github.com/bayplan/bayplan/internal/notify"
)

// NotificationsHandler manages channels, rules and the delivery history (global
// admin only: channels carry URLs with tokens and rules affect the whole
// instance).
type NotificationsHandler struct {
	DB     *pgxpool.Pool
	Logger *slog.Logger
	Store  *notify.Store
	Engine *notify.Engine
}

type channelResponse struct {
	ID        int64                `json:"id"`
	Name      string               `json:"name"`
	Type      notify.ChannelType   `json:"type"`
	Config    notify.ChannelConfig `json:"config"`
	SecretSet bool                 `json:"secretSet"`
	Enabled   bool                 `json:"enabled"`
	CreatedAt time.Time            `json:"createdAt"`
	UpdatedAt time.Time            `json:"updatedAt"`
}

func toChannelResponse(c notify.Channel) channelResponse {
	if c.Config.To == nil {
		c.Config.To = []string{}
	}
	return channelResponse{ID: c.ID, Name: c.Name, Type: c.Type, Config: c.Config, SecretSet: c.SecretSet, Enabled: c.Enabled, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt}
}

// channelInput: create and edit. An empty or omitted secret on edit = keep the
// current one.
type channelInput struct {
	Name    *string               `json:"name"`
	Type    notify.ChannelType    `json:"type"`
	Config  *notify.ChannelConfig `json:"config"`
	Secret  *notify.ChannelSecret `json:"secret"`
	Enabled *bool                 `json:"enabled"`
}

func (in *channelInput) secretGiven() bool {
	return in.Secret != nil && (in.Secret.URL != "" || in.Secret.HeaderValue != "")
}

func normalizeConfig(c *notify.ChannelConfig) {
	to := c.To[:0]
	for _, t := range c.To {
		if t = strings.TrimSpace(t); t != "" {
			to = append(to, t)
		}
	}
	c.To = to
	c.HeaderName = strings.TrimSpace(c.HeaderName)
}

func (h *NotificationsHandler) ListChannels(w http.ResponseWriter, r *http.Request) {
	list, err := h.Store.Channels(r.Context())
	if err != nil {
		h.Logger.Error("notify.channels: query failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	out := make([]channelResponse, 0, len(list))
	for _, c := range list {
		out = append(out, toChannelResponse(c))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *NotificationsHandler) CreateChannel(w http.ResponseWriter, r *http.Request) {
	var in channelInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if in.Name == nil || in.Config == nil {
		http.Error(w, "name, type and config are required", http.StatusBadRequest)
		return
	}
	ch := notify.Channel{Name: strings.TrimSpace(*in.Name), Type: in.Type, Config: *in.Config, Enabled: true}
	if in.Secret != nil {
		ch.Secret = *in.Secret
		ch.Secret.URL = strings.TrimSpace(ch.Secret.URL)
	}
	if in.Enabled != nil {
		ch.Enabled = *in.Enabled
	}
	normalizeConfig(&ch.Config)
	if err := ch.Validate(true); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	id, err := h.Store.CreateChannel(r.Context(), &ch)
	if errors.Is(err, notify.ErrDuplicate) {
		http.Error(w, "a channel with that name already exists", http.StatusConflict)
		return
	}
	if err != nil {
		h.Logger.Error("notify.channels: insert failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.audit(r, "notification_channel.create", map[string]any{"name": ch.Name, "type": ch.Type})
	created, err := h.Store.Channel(r.Context(), id)
	if err != nil {
		h.Logger.Error("notify.channels: re-read failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, toChannelResponse(*created))
}

func (h *NotificationsHandler) UpdateChannel(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	var in channelInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	ch, err := h.Store.Channel(r.Context(), id)
	if errors.Is(err, notify.ErrNotFound) {
		http.Error(w, "channel not found", http.StatusNotFound)
		return
	}
	if err != nil {
		h.Logger.Error("notify.channels: load failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if in.Name != nil {
		ch.Name = strings.TrimSpace(*in.Name)
	}
	if in.Config != nil {
		ch.Config = *in.Config
		normalizeConfig(&ch.Config)
	}
	if in.Enabled != nil {
		ch.Enabled = *in.Enabled
	}
	if in.secretGiven() {
		if in.Secret.URL != "" {
			ch.Secret.URL = strings.TrimSpace(in.Secret.URL)
		}
		if in.Secret.HeaderValue != "" {
			ch.Secret.HeaderValue = in.Secret.HeaderValue
		}
	}
	if err := ch.Validate(ch.Type != notify.TypeEmail); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.Store.UpdateChannel(r.Context(), ch); err != nil {
		if errors.Is(err, notify.ErrDuplicate) {
			http.Error(w, "a channel with that name already exists", http.StatusConflict)
			return
		}
		h.Logger.Error("notify.channels: save failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	ch.SecretSet = ch.Secret.URL != "" || ch.Secret.HeaderValue != ""
	ch.UpdatedAt = time.Now()
	h.audit(r, "notification_channel.update", map[string]any{"name": ch.Name, "enabled": ch.Enabled})
	writeJSON(w, http.StatusOK, toChannelResponse(*ch))
}

func (h *NotificationsHandler) DeleteChannel(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	ch, err := h.Store.Channel(r.Context(), id)
	if errors.Is(err, notify.ErrNotFound) {
		http.Error(w, "channel not found", http.StatusNotFound)
		return
	}
	if err != nil {
		h.Logger.Error("notify.channels: load failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := h.Store.DeleteChannel(r.Context(), id); err != nil {
		h.Logger.Error("notify.channels: delete failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.audit(r, "notification_channel.delete", map[string]any{"name": ch.Name})
	w.WriteHeader(http.StatusNoContent)
}

// TestChannel sends a test message. The destination's error is returned
// verbatim: it is the real explanation (Slack 404, SMTP auth...).
func (h *NotificationsHandler) TestChannel(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	ch, err := h.Store.Channel(r.Context(), id)
	if errors.Is(err, notify.ErrNotFound) {
		http.Error(w, "channel not found", http.StatusNotFound)
		return
	}
	if err != nil {
		h.Logger.Error("notify.channels: load failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if err := h.Engine.Test(ctx, *ch); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	h.audit(r, "notification_channel.test", map[string]any{"name": ch.Name})
	w.WriteHeader(http.StatusNoContent)
}

type ruleResponse struct {
	ID                 int64     `json:"id"`
	Name               string    `json:"name"`
	Enabled            bool      `json:"enabled"`
	ClusterID          *int64    `json:"clusterId"`
	Severities         []string  `json:"severities"`
	Kinds              []string  `json:"kinds"`
	Reasons            []string  `json:"reasons"`
	Namespaces         []string  `json:"namespaces"`
	MinDurationSeconds int       `json:"minDurationSeconds"`
	CooldownSeconds    int       `json:"cooldownSeconds"`
	NotifyResolved     bool      `json:"notifyResolved"`
	ChannelIDs         []int64   `json:"channelIds"`
	CreatedAt          time.Time `json:"createdAt"`
	UpdatedAt          time.Time `json:"updatedAt"`
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func toRuleResponse(r notify.Rule) ruleResponse {
	if r.ChannelIDs == nil {
		r.ChannelIDs = []int64{}
	}
	return ruleResponse{
		ID: r.ID, Name: r.Name, Enabled: r.Enabled, ClusterID: r.ClusterID,
		Severities: nonNil(r.Severities), Kinds: nonNil(r.Kinds), Reasons: nonNil(r.Reasons), Namespaces: nonNil(r.Namespaces),
		MinDurationSeconds: int(r.MinDuration / time.Second), CooldownSeconds: int(r.Cooldown / time.Second),
		NotifyResolved: r.NotifyResolved, ChannelIDs: r.ChannelIDs, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

// ruleInput: everything optional on edit (merged over the current rule).
type ruleInput struct {
	Name               *string   `json:"name"`
	Enabled            *bool     `json:"enabled"`
	ClusterID          *int64    `json:"clusterId"`
	ClearCluster       bool      `json:"clearCluster"`
	Severities         *[]string `json:"severities"`
	Kinds              *[]string `json:"kinds"`
	Reasons            *[]string `json:"reasons"`
	Namespaces         *[]string `json:"namespaces"`
	MinDurationSeconds *int      `json:"minDurationSeconds"`
	CooldownSeconds    *int      `json:"cooldownSeconds"`
	NotifyResolved     *bool     `json:"notifyResolved"`
	ChannelIDs         *[]int64  `json:"channelIds"`
}

func (in *ruleInput) applyTo(r *notify.Rule) {
	if in.Name != nil {
		r.Name = strings.TrimSpace(*in.Name)
	}
	if in.Enabled != nil {
		r.Enabled = *in.Enabled
	}
	if in.ClearCluster {
		r.ClusterID = nil
	} else if in.ClusterID != nil {
		r.ClusterID = in.ClusterID
	}
	if in.Severities != nil {
		r.Severities = *in.Severities
	}
	if in.Kinds != nil {
		r.Kinds = *in.Kinds
	}
	if in.Reasons != nil {
		r.Reasons = *in.Reasons
	}
	if in.Namespaces != nil {
		r.Namespaces = *in.Namespaces
	}
	if in.MinDurationSeconds != nil {
		r.MinDuration = time.Duration(*in.MinDurationSeconds) * time.Second
	}
	if in.CooldownSeconds != nil {
		r.Cooldown = time.Duration(*in.CooldownSeconds) * time.Second
	}
	if in.NotifyResolved != nil {
		r.NotifyResolved = *in.NotifyResolved
	}
	if in.ChannelIDs != nil {
		r.ChannelIDs = *in.ChannelIDs
	}
}

// channelsExist rejects a rule pointing at channels that do not exist:
// channel_ids is JSONB, not an FK, so nothing else would stop it, and the
// rule would look configured while never delivering anything.
func (h *NotificationsHandler) channelsExist(w http.ResponseWriter, r *http.Request, ids []int64) bool {
	channels, err := h.Store.Channels(r.Context())
	if err != nil {
		h.Logger.Error("notify.rules: channel lookup failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return false
	}
	known := make(map[int64]bool, len(channels))
	for _, c := range channels {
		known[c.ID] = true
	}
	for _, id := range ids {
		if !known[id] {
			http.Error(w, fmt.Sprintf("channel %d does not exist", id), http.StatusBadRequest)
			return false
		}
	}
	return true
}

func (h *NotificationsHandler) ListRules(w http.ResponseWriter, r *http.Request) {
	list, err := h.Store.Rules(r.Context(), false)
	if err != nil {
		h.Logger.Error("notify.rules: query failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	out := make([]ruleResponse, 0, len(list))
	for _, x := range list {
		out = append(out, toRuleResponse(x))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *NotificationsHandler) CreateRule(w http.ResponseWriter, r *http.Request) {
	var in ruleInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	rule := notify.Rule{Enabled: true, MinDuration: 5 * time.Minute, Cooldown: time.Hour, NotifyResolved: true}
	in.applyTo(&rule)
	if err := rule.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !h.channelsExist(w, r, rule.ChannelIDs) {
		return
	}
	id, err := h.Store.CreateRule(r.Context(), &rule)
	if errors.Is(err, notify.ErrDuplicate) {
		http.Error(w, "a rule with that name already exists", http.StatusConflict)
		return
	}
	if err != nil {
		// A non-existent cluster is the client's fault, not an internal failure.
		if errors.Is(err, notify.ErrClusterNotFound) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		h.Logger.Error("notify.rules: insert failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.audit(r, "notification_rule.create", map[string]any{"name": rule.Name})
	created, err := h.Store.Rule(r.Context(), id)
	if err != nil {
		h.Logger.Error("notify.rules: re-read failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, toRuleResponse(*created))
}

func (h *NotificationsHandler) UpdateRule(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	var in ruleInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	rule, err := h.Store.Rule(r.Context(), id)
	if errors.Is(err, notify.ErrNotFound) {
		http.Error(w, "rule not found", http.StatusNotFound)
		return
	}
	if err != nil {
		h.Logger.Error("notify.rules: load failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	in.applyTo(rule)
	if err := rule.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if !h.channelsExist(w, r, rule.ChannelIDs) {
		return
	}
	if err := h.Store.UpdateRule(r.Context(), rule); err != nil {
		if errors.Is(err, notify.ErrDuplicate) {
			http.Error(w, "a rule with that name already exists", http.StatusConflict)
			return
		}
		h.Logger.Error("notify.rules: save failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	rule.UpdatedAt = time.Now()
	h.audit(r, "notification_rule.update", map[string]any{"name": rule.Name, "enabled": rule.Enabled})
	writeJSON(w, http.StatusOK, toRuleResponse(*rule))
}

func (h *NotificationsHandler) DeleteRule(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	rule, err := h.Store.Rule(r.Context(), id)
	if errors.Is(err, notify.ErrNotFound) {
		http.Error(w, "rule not found", http.StatusNotFound)
		return
	}
	if err != nil {
		h.Logger.Error("notify.rules: load failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if err := h.Store.DeleteRule(r.Context(), id); err != nil {
		h.Logger.Error("notify.rules: delete failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.audit(r, "notification_rule.delete", map[string]any{"name": rule.Name})
	w.WriteHeader(http.StatusNoContent)
}

// Deliveries returns the latest deliveries (100 by default, 500 max).
func (h *NotificationsHandler) Deliveries(w http.ResponseWriter, r *http.Request) {
	limit := 100
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v <= 500 {
		limit = v
	}
	list, err := h.Store.Deliveries(r.Context(), limit)
	if err != nil {
		h.Logger.Error("notify.deliveries: query failed", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// Options returns the lists for the UI selectors.
func (h *NotificationsHandler) Options(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"reasons": notify.KnownReasons, "kinds": notify.KnownKinds})
}

func (h *NotificationsHandler) audit(r *http.Request, action string, detail map[string]any) {
	var actorID *int64
	if sess, ok := middleware.SessionFromContext(r.Context()); ok {
		actorID = &sess.UserID
	}
	audit.Log(r.Context(), h.DB, h.Logger, audit.Entry{UserID: actorID, IP: middleware.ClientIP(r), Action: action, Result: audit.ResultSuccess, Detail: detail})
}
