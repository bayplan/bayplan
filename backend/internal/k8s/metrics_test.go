package k8s

import "testing"

func TestParseUsage(t *testing.T) {
	u := parseUsage(map[string]string{"cpu": "250m", "memory": "128Mi"})
	if u.CPUMilli != 250 || u.MemoryBytes != 128*1024*1024 {
		t.Fatalf("got %+v", u)
	}
	// metrics-server returns CPU in nanocores and memory in Ki.
	u = parseUsage(map[string]string{"cpu": "1500000n", "memory": "2048Ki"})
	if u.CPUMilli != 2 || u.MemoryBytes != 2048*1024 {
		t.Fatalf("nanocores/Ki: got %+v", u)
	}
	// An unreadable value does not bring down the whole snapshot: it counts as 0.
	u = parseUsage(map[string]string{"cpu": "abc"})
	if u.CPUMilli != 0 {
		t.Fatalf("got %+v", u)
	}
}
