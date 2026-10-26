package netguard

import (
	"net"
	"testing"
)

func TestIsMetadata(t *testing.T) {
	blocked := []string{"169.254.169.254", "::ffff:169.254.169.254", "fe80::1", "fd00:ec2::254", "100.100.100.200"}
	for _, s := range blocked {
		if !IsMetadata(net.ParseIP(s)) {
			t.Errorf("%s should be blocked", s)
		}
	}
	allowed := []string{"10.0.0.5", "192.168.1.1", "8.8.8.8", "2001:db8::1", "100.100.100.201"}
	for _, s := range allowed {
		if IsMetadata(net.ParseIP(s)) {
			t.Errorf("%s should be allowed", s)
		}
	}
	if IsMetadata(nil) {
		t.Error("nil is not an address")
	}
}
