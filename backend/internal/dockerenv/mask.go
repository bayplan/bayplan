package dockerenv

import "encoding/json"

// maskedValue (no angle brackets: json.Marshal would escape them) replaces the value of every environment variable in an inspect
// document shown to someone below admin. The key stays: knowing which
// variables a container has is operational information, their values are
// the host's credentials (POSTGRES_PASSWORD, API keys).
const maskedValue = "redacted"

// MaskInspect returns the inspect JSON with Config.Env values replaced by
// maskedValue. Args and Cmd are left: they are what `docker ps` shows anyway.
func MaskInspect(raw []byte) ([]byte, error) {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	if cfg, ok := doc["Config"].(map[string]any); ok {
		if env, ok := cfg["Env"].([]any); ok {
			for i, e := range env {
				s, _ := e.(string)
				key := s
				for j := 0; j < len(s); j++ {
					if s[j] == '=' {
						key = s[:j]
						break
					}
				}
				env[i] = key + "=" + maskedValue
			}
		}
	}
	return json.Marshal(doc)
}
