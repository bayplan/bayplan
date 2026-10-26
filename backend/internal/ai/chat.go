package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// The assistant needs tool calling, and each provider family carries it in
// its own message shapes. A Chat keeps the conversation in the provider's
// NATIVE form: what the model returns is appended back unchanged, because
// Anthropic's thinking blocks and Gemini's thought signatures must be replayed
// exactly or the next turn is rejected. Only the caller-facing types below are
// common.

type ToolDef struct {
	Name        string
	Description string
	Schema      map[string]any
}

type ToolCall struct {
	ID   string
	Name string
	Args map[string]any
}

type ToolResult struct {
	Call    ToolCall
	Content string
	IsError bool
}

type Step struct {
	Text         string
	Calls        []ToolCall
	InputTokens  int64
	OutputTokens int64
}

// Chat is one conversation with one provider.
type Chat interface {
	// AddUser and AddAssistant replay earlier turns (plain text: the browser
	// keeps the history, the server keeps nothing).
	AddUser(text string)
	AddAssistant(text string)
	// Step calls the model once and appends its answer to the conversation.
	Step(ctx context.Context) (Step, error)
	// AddToolResults answers the calls of the last step.
	AddToolResults(results []ToolResult)
}

func (c *Client) NewChat(cfg Config, system string, tools []ToolDef) (Chat, error) {
	switch cfg.Provider {
	case ProviderOpenAI:
		return &openAIChat{c: c, cfg: cfg, tools: tools, messages: []any{map[string]any{"role": "system", "content": system}}}, nil
	case ProviderAnthropic:
		return &anthropicChat{c: c, cfg: cfg, system: system, tools: tools}, nil
	case ProviderGemini:
		return &geminiChat{c: c, cfg: cfg, system: system, tools: tools}, nil
	}
	return nil, fmt.Errorf("unknown AI provider %q", cfg.Provider)
}

// portableSchema keeps the JSON Schema subset all three accept: no type
// unions (a cluster "id or name" becomes a string) and no
// additionalProperties, which Gemini rejects.
func portableSchema(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, child := range t {
			switch {
			case k == "additionalProperties":
				continue
			case k == "type":
				if list, ok := child.([]string); ok && len(list) > 0 {
					out[k] = "string"
					continue
				}
				if list, ok := child.([]any); ok && len(list) > 0 {
					out[k] = "string"
					continue
				}
				out[k] = child
			default:
				out[k] = portableSchema(child)
			}
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, child := range t {
			out[i] = portableSchema(child)
		}
		return out
	}
	return v
}

// --- OpenAI-compatible (Chat Completions with tools) ---

type openAIChat struct {
	c        *Client
	cfg      Config
	tools    []ToolDef
	messages []any
}

func (o *openAIChat) AddUser(text string) {
	o.messages = append(o.messages, map[string]any{"role": "user", "content": text})
}

func (o *openAIChat) AddAssistant(text string) {
	o.messages = append(o.messages, map[string]any{"role": "assistant", "content": text})
}

func (o *openAIChat) Step(ctx context.Context) (Step, error) {
	tools := make([]any, 0, len(o.tools))
	for _, t := range o.tools {
		tools = append(tools, map[string]any{"type": "function", "function": map[string]any{
			"name": t.Name, "description": t.Description, "parameters": portableSchema(t.Schema),
		}})
	}
	body := map[string]any{"model": o.cfg.Model, "max_completion_tokens": maxOutputTokens, "messages": o.messages}
	if len(tools) > 0 {
		body["tools"] = tools
	}
	var out struct {
		Choices []struct {
			Message json.RawMessage `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := o.c.post(ctx, openAIBase(o.cfg)+"/chat/completions", openAIHeaders(o.cfg), body, &out); err != nil {
		return Step{}, err
	}
	if len(out.Choices) == 0 {
		return Step{}, errors.New("the AI provider returned no answer")
	}
	var msg struct {
		Content   *string `json:"content"`
		Refusal   string  `json:"refusal"`
		ToolCalls []struct {
			ID       string `json:"id"`
			Function struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
	}
	if err := json.Unmarshal(out.Choices[0].Message, &msg); err != nil {
		return Step{}, err
	}
	o.messages = append(o.messages, out.Choices[0].Message)
	step := Step{InputTokens: out.Usage.PromptTokens, OutputTokens: out.Usage.CompletionTokens}
	if msg.Content != nil {
		step.Text = *msg.Content
	}
	if step.Text == "" && msg.Refusal != "" && len(msg.ToolCalls) == 0 {
		return step, fmt.Errorf("the model declined to answer: %s", msg.Refusal)
	}
	for _, tc := range msg.ToolCalls {
		args := map[string]any{}
		_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)
		step.Calls = append(step.Calls, ToolCall{ID: tc.ID, Name: tc.Function.Name, Args: args})
	}
	return step, nil
}

func (o *openAIChat) AddToolResults(results []ToolResult) {
	for _, r := range results {
		o.messages = append(o.messages, map[string]any{"role": "tool", "tool_call_id": r.Call.ID, "content": r.Content})
	}
}

func openAIBase(cfg Config) string {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	return base
}

func openAIHeaders(cfg Config) map[string]string {
	headers := map[string]string{}
	if cfg.APIKey != "" {
		headers["Authorization"] = "Bearer " + cfg.APIKey
		// Azure OpenAI authenticates keys with api-key instead.
		if u, err := url.Parse(openAIBase(cfg)); err == nil && (strings.HasSuffix(u.Hostname(), ".openai.azure.com") || strings.HasSuffix(u.Hostname(), ".cognitiveservices.azure.com")) {
			headers["api-key"] = cfg.APIKey
		}
	}
	return headers
}

// --- Anthropic (Messages API with tools) ---

type anthropicChat struct {
	c        *Client
	cfg      Config
	system   string
	tools    []ToolDef
	messages []any
}

func (a *anthropicChat) AddUser(text string) {
	a.messages = append(a.messages, map[string]any{"role": "user", "content": text})
}

func (a *anthropicChat) AddAssistant(text string) {
	a.messages = append(a.messages, map[string]any{"role": "assistant", "content": text})
}

func (a *anthropicChat) Step(ctx context.Context) (Step, error) {
	tools := make([]any, 0, len(a.tools))
	for _, t := range a.tools {
		tools = append(tools, map[string]any{"name": t.Name, "description": t.Description, "input_schema": portableSchema(t.Schema)})
	}
	headers, body := anthropicRequest(a.cfg, a.system, a.messages)
	if len(tools) > 0 {
		body["tools"] = tools
	}
	var out struct {
		Content    []json.RawMessage `json:"content"`
		StopReason string            `json:"stop_reason"`
		Usage      struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := a.c.post(ctx, strings.TrimRight(a.c.AnthropicURL, "/")+"/v1/messages", headers, body, &out); err != nil {
		return Step{}, err
	}
	if out.StopReason == "refusal" {
		return Step{}, errors.New("the model declined to answer this request")
	}
	// The whole content goes back unchanged: thinking blocks included.
	a.messages = append(a.messages, map[string]any{"role": "assistant", "content": out.Content})
	step := Step{InputTokens: out.Usage.InputTokens, OutputTokens: out.Usage.OutputTokens}
	var text strings.Builder
	for _, raw := range out.Content {
		var block struct {
			Type  string         `json:"type"`
			Text  string         `json:"text"`
			ID    string         `json:"id"`
			Name  string         `json:"name"`
			Input map[string]any `json:"input"`
		}
		if json.Unmarshal(raw, &block) != nil {
			continue
		}
		switch block.Type {
		case "text":
			text.WriteString(block.Text)
		case "tool_use":
			step.Calls = append(step.Calls, ToolCall{ID: block.ID, Name: block.Name, Args: block.Input})
		}
	}
	step.Text = text.String()
	return step, nil
}

func (a *anthropicChat) AddToolResults(results []ToolResult) {
	blocks := make([]any, 0, len(results))
	for _, r := range results {
		blocks = append(blocks, map[string]any{"type": "tool_result", "tool_use_id": r.Call.ID, "content": r.Content, "is_error": r.IsError})
	}
	// All the results of one turn in ONE user message: splitting them teaches
	// the model to stop calling tools in parallel.
	a.messages = append(a.messages, map[string]any{"role": "user", "content": blocks})
}

func anthropicRequest(cfg Config, system string, messages []any) (map[string]string, map[string]any) {
	headers := map[string]string{"x-api-key": cfg.APIKey, "anthropic-version": "2023-06-01"}
	body := map[string]any{"model": cfg.Model, "max_tokens": maxOutputTokens, "system": system, "messages": messages}
	if anthropicFallbackModels[cfg.Model] {
		headers["anthropic-beta"] = "server-side-fallback-2026-07-01"
		body["fallbacks"] = "default"
	}
	return headers, body
}

// --- Gemini (generateContent with function declarations) ---

type geminiChat struct {
	c        *Client
	cfg      Config
	system   string
	tools    []ToolDef
	contents []any
}

func (g *geminiChat) AddUser(text string) {
	g.contents = append(g.contents, map[string]any{"role": "user", "parts": []any{map[string]any{"text": text}}})
}

func (g *geminiChat) AddAssistant(text string) {
	g.contents = append(g.contents, map[string]any{"role": "model", "parts": []any{map[string]any{"text": text}}})
}

func (g *geminiChat) Step(ctx context.Context) (Step, error) {
	decls := make([]any, 0, len(g.tools))
	for _, t := range g.tools {
		decls = append(decls, map[string]any{"name": t.Name, "description": t.Description, "parameters": portableSchema(t.Schema)})
	}
	body := map[string]any{
		"systemInstruction": map[string]any{"parts": []any{map[string]any{"text": g.system}}},
		"contents":          g.contents,
		"generationConfig":  map[string]any{"maxOutputTokens": maxOutputTokens},
	}
	if len(decls) > 0 {
		body["tools"] = []any{map[string]any{"functionDeclarations": decls}}
	}
	var out struct {
		Candidates []struct {
			Content json.RawMessage `json:"content"`
		} `json:"candidates"`
		PromptFeedback struct {
			BlockReason string `json:"blockReason"`
		} `json:"promptFeedback"`
		UsageMetadata struct {
			PromptTokenCount     int64 `json:"promptTokenCount"`
			CandidatesTokenCount int64 `json:"candidatesTokenCount"`
		} `json:"usageMetadata"`
	}
	endpoint := strings.TrimRight(g.c.GeminiURL, "/") + "/v1beta/models/" + url.PathEscape(g.cfg.Model) + ":generateContent"
	if err := g.c.post(ctx, endpoint, map[string]string{"x-goog-api-key": g.cfg.APIKey}, body, &out); err != nil {
		return Step{}, err
	}
	if out.PromptFeedback.BlockReason != "" {
		return Step{}, fmt.Errorf("the model declined to answer (%s)", out.PromptFeedback.BlockReason)
	}
	if len(out.Candidates) == 0 {
		return Step{}, errors.New("the AI provider returned no answer")
	}
	// Replayed unchanged: parts may carry thought signatures.
	g.contents = append(g.contents, out.Candidates[0].Content)
	var content struct {
		Parts []struct {
			Text         string `json:"text"`
			Thought      bool   `json:"thought"`
			FunctionCall *struct {
				ID   string         `json:"id"`
				Name string         `json:"name"`
				Args map[string]any `json:"args"`
			} `json:"functionCall"`
		} `json:"parts"`
	}
	_ = json.Unmarshal(out.Candidates[0].Content, &content)
	step := Step{InputTokens: out.UsageMetadata.PromptTokenCount, OutputTokens: out.UsageMetadata.CandidatesTokenCount}
	var text strings.Builder
	for _, p := range content.Parts {
		switch {
		case p.FunctionCall != nil:
			step.Calls = append(step.Calls, ToolCall{ID: p.FunctionCall.ID, Name: p.FunctionCall.Name, Args: p.FunctionCall.Args})
		case !p.Thought:
			text.WriteString(p.Text)
		}
	}
	step.Text = text.String()
	return step, nil
}

func (g *geminiChat) AddToolResults(results []ToolResult) {
	parts := make([]any, 0, len(results))
	for _, r := range results {
		resp := map[string]any{"name": r.Call.Name, "response": map[string]any{"result": r.Content}}
		if r.Call.ID != "" {
			resp["id"] = r.Call.ID
		}
		parts = append(parts, map[string]any{"functionResponse": resp})
	}
	g.contents = append(g.contents, map[string]any{"role": "user", "parts": parts})
}
