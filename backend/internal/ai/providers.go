// Package ai talks to the LLM provider the admin configured (settings "ai")
// for "Explain": one request, one answer, no tools. Raw HTTP for the three
// provider families instead of three SDKs, like the rest of the panel's
// integrations: each one is a single POST.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/bayplan/bayplan/internal/netguard"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

const (
	ProviderOpenAI    = "openai"
	ProviderAnthropic = "anthropic"
	ProviderGemini    = "gemini"

	// Adaptive thinking on current models can take a while on a messy object;
	// the UI shows a spinner, so a generous ceiling beats a false failure.
	requestTimeout = 3 * time.Minute
	// Thinking counts toward this cap on current models: room for it and the
	// answer, still well under the non-streaming request timeout.
	maxOutputTokens  = 16000
	maxResponseBytes = 4 << 20
)

// DefaultAnthropicModel is what the settings form proposes for Anthropic. For
// the other families the admin types the model their account or server has:
// model names there change too often to hard-code one.
const DefaultAnthropicModel = "claude-opus-5-5"

type Config struct {
	Provider string
	BaseURL  string // openai only
	Model    string
	APIKey   string
}

type Result struct {
	Text         string
	InputTokens  int64
	OutputTokens int64
}

// Client is swappable in tests; production uses NewHTTPClient.
type Client struct {
	HTTP *http.Client
	// Endpoints overridable in tests.
	AnthropicURL string
	GeminiURL    string
}

func NewClient() *Client {
	return &Client{
		HTTP: &http.Client{
			Timeout: requestTimeout,
			// x-api-key / x-goog-api-key / api-key are not Authorization: Go
			// would keep them across a cross-host redirect, so none is followed.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			Transport: &http.Transport{
				Proxy:               http.ProxyFromEnvironment,
				DialContext:         guardedDialer().DialContext,
				TLSHandshakeTimeout: 10 * time.Second,
			},
		},
		AnthropicURL: "https://api.anthropic.com",
		GeminiURL:    "https://generativelanguage.googleapis.com",
	}
}

// guardedDialer refuses link-local addresses once resolved: the OpenAI-
// compatible base URL is typed by an admin, and the cloud metadata endpoint
// must not be reachable through it (same rule as webhooks and registries).
func guardedDialer() *net.Dialer {
	return &net.Dialer{
		Timeout: 10 * time.Second,
		ControlContext: func(_ context.Context, _, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			if ip := net.ParseIP(host); ip != nil && netguard.IsMetadata(ip) {
				return errors.New("the AI endpoint points to a link-local address (cloud metadata); not allowed")
			}
			return nil
		},
	}
}

// ValidateBaseURL checks an OpenAI-compatible base URL before saving it.
func ValidateBaseURL(raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("the base URL must be an absolute http(s) URL, e.g. https://api.openai.com/v1")
	}
	return nil
}

// Complete sends one system + user prompt and returns the text answer.
// Provider errors come back with the provider's own message: an invalid key,
// an unknown model or an exhausted quota is something the admin must read.
func (c *Client) Complete(ctx context.Context, cfg Config, system, user string) (Result, error) {
	switch cfg.Provider {
	case ProviderOpenAI:
		return c.openAI(ctx, cfg, system, user)
	case ProviderAnthropic:
		return c.anthropic(ctx, cfg, system, user)
	case ProviderGemini:
		return c.gemini(ctx, cfg, system, user)
	}
	return Result{}, fmt.Errorf("unknown AI provider %q", cfg.Provider)
}

func (c *Client) post(ctx context.Context, endpoint string, headers map[string]string, body any, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		var uerr *url.Error
		if errors.As(err, &uerr) {
			// The URL may carry nothing secret, but the provider's error is
			// clearer without our full request line.
			return fmt.Errorf("could not reach the AI provider: %w", uerr.Err)
		}
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("the AI provider answered %d: %s", resp.StatusCode, providerMessage(data))
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("unexpected answer from the AI provider: %w", err)
	}
	return nil
}

// providerMessage extracts the human message from the three error shapes
// ({"error":{"message"}} for OpenAI/Gemini, {"error":{"type","message"}} for
// Anthropic), falling back to a trimmed body.
func providerMessage(data []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &e) == nil && e.Error.Message != "" {
		if e.Error.Type != "" {
			return e.Error.Type + ": " + e.Error.Message
		}
		return e.Error.Message
	}
	msg := strings.TrimSpace(string(data))
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	return msg
}

func (c *Client) openAI(ctx context.Context, cfg Config, system, user string) (Result, error) {
	base, headers := openAIBase(cfg), openAIHeaders(cfg)
	// Chat Completions and not the Responses API: it is the one every
	// compatible server (Ollama, vLLM, LiteLLM, Azure) implements.
	body := map[string]any{
		"model":                 cfg.Model,
		"max_completion_tokens": maxOutputTokens,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
				Refusal string `json:"refusal"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := c.post(ctx, base+"/chat/completions", headers, body, &out); err != nil {
		return Result{}, err
	}
	if len(out.Choices) == 0 {
		return Result{}, errors.New("the AI provider returned no answer")
	}
	msg := out.Choices[0].Message
	if msg.Content == "" && msg.Refusal != "" {
		return Result{}, fmt.Errorf("the model declined to answer: %s", msg.Refusal)
	}
	return Result{Text: msg.Content, InputTokens: out.Usage.PromptTokens, OutputTokens: out.Usage.CompletionTokens}, nil
}

// anthropicFallbackModels accept `fallbacks: "default"`: if a safety
// classifier declines, the API re-runs the request on its recommended model
// instead of returning a refusal. Other models would answer 400 to the field.
var anthropicFallbackModels = map[string]bool{
	"claude-opus-5-5": true, "claude-opus-5": true, "claude-fable-5-1": true, "claude-sonnet-5-5": true,
}

func (c *Client) anthropic(ctx context.Context, cfg Config, system, user string) (Result, error) {
	headers, body := anthropicRequest(cfg, system, []any{map[string]string{"role": "user", "content": user}})
	var out struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := c.post(ctx, strings.TrimRight(c.AnthropicURL, "/")+"/v1/messages", headers, body, &out); err != nil {
		return Result{}, err
	}
	if out.StopReason == "refusal" {
		return Result{}, errors.New("the model declined to answer this request")
	}
	// Thinking blocks come back too (empty by default): only text is the answer.
	var text strings.Builder
	for _, block := range out.Content {
		if block.Type == "text" {
			text.WriteString(block.Text)
		}
	}
	return Result{Text: text.String(), InputTokens: out.Usage.InputTokens, OutputTokens: out.Usage.OutputTokens}, nil
}

func (c *Client) gemini(ctx context.Context, cfg Config, system, user string) (Result, error) {
	endpoint := strings.TrimRight(c.GeminiURL, "/") + "/v1beta/models/" + url.PathEscape(cfg.Model) + ":generateContent"
	body := map[string]any{
		"systemInstruction": map[string]any{"parts": []map[string]string{{"text": system}}},
		"contents":          []map[string]any{{"role": "user", "parts": []map[string]string{{"text": user}}}},
		"generationConfig":  map[string]any{"maxOutputTokens": maxOutputTokens},
	}
	var out struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text    string `json:"text"`
					Thought bool   `json:"thought"`
				} `json:"parts"`
			} `json:"content"`
			FinishReason string `json:"finishReason"`
		} `json:"candidates"`
		PromptFeedback struct {
			BlockReason string `json:"blockReason"`
		} `json:"promptFeedback"`
		UsageMetadata struct {
			PromptTokenCount     int64 `json:"promptTokenCount"`
			CandidatesTokenCount int64 `json:"candidatesTokenCount"`
		} `json:"usageMetadata"`
	}
	// The key goes in a header, never in the query string, where proxies log it.
	if err := c.post(ctx, endpoint, map[string]string{"x-goog-api-key": cfg.APIKey}, body, &out); err != nil {
		return Result{}, err
	}
	if out.PromptFeedback.BlockReason != "" {
		return Result{}, fmt.Errorf("the model declined to answer (%s)", out.PromptFeedback.BlockReason)
	}
	if len(out.Candidates) == 0 {
		return Result{}, errors.New("the AI provider returned no answer")
	}
	var text strings.Builder
	for _, part := range out.Candidates[0].Content.Parts {
		if !part.Thought {
			text.WriteString(part.Text)
		}
	}
	return Result{Text: text.String(), InputTokens: out.UsageMetadata.PromptTokenCount, OutputTokens: out.UsageMetadata.CandidatesTokenCount}, nil
}
