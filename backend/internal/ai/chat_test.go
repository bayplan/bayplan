package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// scripted answers one canned response per request and keeps every request
// body, so a test can check what was replayed on the second turn.
func scripted(t *testing.T, answers ...string) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var bodies []map[string]any
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var b map[string]any
		_ = json.Unmarshal(raw, &b)
		bodies = append(bodies, b)
		_, _ = w.Write([]byte(answers[n]))
		n++
	}))
	t.Cleanup(srv.Close)
	return srv, &bodies
}

var listPods = ToolDef{Name: "list_resources", Description: "list", Schema: map[string]any{
	"type": "object", "additionalProperties": false,
	"properties": map[string]any{"cluster": map[string]any{"type": []string{"string", "integer"}}},
}}

// runTurn drives one user question through a tool round and back.
func runTurn(t *testing.T, chat Chat) (Step, Step) {
	t.Helper()
	chat.AddUser("what is failing?")
	first, err := chat.Step(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Calls) != 1 || first.Calls[0].Name != "list_resources" || first.Calls[0].Args["cluster"] != "prod" {
		t.Fatalf("first step: %+v", first)
	}
	chat.AddToolResults([]ToolResult{{Call: first.Calls[0], Content: "pod api-1 CrashLoopBackOff"}})
	second, err := chat.Step(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second.Text != "api-1 is crashing" || len(second.Calls) != 0 {
		t.Fatalf("second step: %+v", second)
	}
	return first, second
}

func TestAnthropicChatReplaysThinking(t *testing.T) {
	srv, bodies := scripted(t,
		`{"content":[{"type":"thinking","thinking":"","signature":"sig-1"},{"type":"tool_use","id":"tu_1","name":"list_resources","input":{"cluster":"prod"}}],"stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":5}}`,
		`{"content":[{"type":"text","text":"api-1 is crashing"}],"stop_reason":"end_turn","usage":{}}`,
	)
	chat, _ := testClient(srv).NewChat(Config{Provider: ProviderAnthropic, Model: "claude-opus-5-5", APIKey: "k"}, "sys", []ToolDef{listPods})
	runTurn(t, chat)
	second := (*bodies)[1]
	msgs := second["messages"].([]any)
	assistant := msgs[1].(map[string]any)["content"].([]any)
	if assistant[0].(map[string]any)["signature"] != "sig-1" {
		t.Fatalf("thinking block not replayed unchanged: %v", assistant)
	}
	result := msgs[2].(map[string]any)["content"].([]any)[0].(map[string]any)
	if result["type"] != "tool_result" || result["tool_use_id"] != "tu_1" {
		t.Fatalf("tool result: %v", result)
	}
	tool := (*bodies)[0]["tools"].([]any)[0].(map[string]any)
	schema := tool["input_schema"].(map[string]any)
	if _, has := schema["additionalProperties"]; has || schema["properties"].(map[string]any)["cluster"].(map[string]any)["type"] != "string" {
		t.Fatalf("schema not made portable: %v", schema)
	}
}

func TestOpenAIChat(t *testing.T) {
	srv, bodies := scripted(t,
		`{"choices":[{"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"list_resources","arguments":"{\"cluster\":\"prod\"}"}}]}}],"usage":{}}`,
		`{"choices":[{"message":{"role":"assistant","content":"api-1 is crashing"}}],"usage":{}}`,
	)
	chat, _ := testClient(srv).NewChat(Config{Provider: ProviderOpenAI, BaseURL: srv.URL, Model: "m"}, "sys", []ToolDef{listPods})
	runTurn(t, chat)
	msgs := (*bodies)[1]["messages"].([]any)
	if msgs[0].(map[string]any)["role"] != "system" || msgs[2].(map[string]any)["tool_calls"] == nil {
		t.Fatalf("history: %v", msgs)
	}
	toolMsg := msgs[3].(map[string]any)
	if toolMsg["role"] != "tool" || toolMsg["tool_call_id"] != "call_1" || !strings.Contains(toolMsg["content"].(string), "CrashLoop") {
		t.Fatalf("tool message: %v", toolMsg)
	}
}

func TestGeminiChatReplaysSignatures(t *testing.T) {
	srv, bodies := scripted(t,
		`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"list_resources","args":{"cluster":"prod"}},"thoughtSignature":"ts-1"}]}}],"usageMetadata":{}}`,
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"api-1 is crashing"}]}}],"usageMetadata":{}}`,
	)
	chat, _ := testClient(srv).NewChat(Config{Provider: ProviderGemini, Model: "g", APIKey: "k"}, "sys", []ToolDef{listPods})
	runTurn(t, chat)
	contents := (*bodies)[1]["contents"].([]any)
	model := contents[1].(map[string]any)["parts"].([]any)[0].(map[string]any)
	if model["thoughtSignature"] != "ts-1" {
		t.Fatalf("thought signature not replayed: %v", model)
	}
	resp := contents[2].(map[string]any)["parts"].([]any)[0].(map[string]any)["functionResponse"].(map[string]any)
	if resp["name"] != "list_resources" {
		t.Fatalf("function response: %v", resp)
	}
}
