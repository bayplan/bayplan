package api

import (
	"errors"
	"testing"

	"github.com/bayplan/bayplan/internal/settings"
)

func fakeEncrypt(s string) (settings.Secret, error) {
	return settings.Secret{KeyID: "v1", Ciphertext: []byte("enc:" + s)}, nil
}

func TestBuildAISettings(t *testing.T) {
	str := func(s string) *string { return &s }
	stored := settings.AI{APIKey: settings.Secret{KeyID: "v1", Ciphertext: []byte("old")}}

	// An empty key keeps the stored one; a new one replaces it.
	got, err := buildAISettings(aiSettingsInput{Enabled: true, Provider: "anthropic", Model: "claude-opus-5-5", APIKey: str("")}, stored, fakeEncrypt)
	if err != nil || string(got.APIKey.Ciphertext) != "old" {
		t.Fatalf("keep: %v %v", got.APIKey, err)
	}
	got, _ = buildAISettings(aiSettingsInput{Provider: "anthropic", Model: "m", APIKey: str("sk-new")}, stored, fakeEncrypt)
	if string(got.APIKey.Ciphertext) != "enc:sk-new" {
		t.Fatalf("replace: %v", got.APIKey)
	}

	// A local OpenAI-compatible server may go without a key; a cloud provider not.
	if _, err := buildAISettings(aiSettingsInput{Enabled: true, Provider: "openai", BaseURL: "http://ollama:11434/v1", Model: "llama3", ClearAPIKey: true}, stored, fakeEncrypt); err != nil {
		t.Fatalf("keyless local server rejected: %v", err)
	}
	invalid := []aiSettingsInput{
		{Enabled: true, Provider: "gemini", Model: "x", ClearAPIKey: true},
		{Enabled: true, Provider: "anthropic"},
		{Provider: "bard"},
		{Provider: "openai", BaseURL: "ftp://x"},
		{Provider: "openai", MinRole: "owner"},
		{Provider: "openai", DailyLimitPerUser: -1},
	}
	for _, in := range invalid {
		var verr *ValidationError
		if _, err := buildAISettings(in, stored, fakeEncrypt); !errors.As(err, &verr) {
			t.Errorf("%+v: expected a validation error, got %v", in, err)
		}
	}
	// The base URL only means something for OpenAI-compatible servers.
	got, _ = buildAISettings(aiSettingsInput{Provider: "anthropic", BaseURL: "https://elsewhere", Model: "m"}, stored, fakeEncrypt)
	if got.BaseURL != "" {
		t.Fatalf("baseURL kept for anthropic: %q", got.BaseURL)
	}
}

func TestAssistantRefusesConfigContents(t *testing.T) {
	for _, kind := range []string{"secrets", "configmaps"} {
		if assistantRefusal("get_resource", map[string]any{"kind": kind}) == "" {
			t.Errorf("get_resource %s allowed", kind)
		}
		if assistantRefusal("list_resources", map[string]any{"kind": kind}) != "" {
			t.Errorf("listing %s refused", kind)
		}
	}
	if assistantRefusal("get_resource", map[string]any{"kind": "deployments"}) != "" {
		t.Error("deployment YAML refused")
	}
}
