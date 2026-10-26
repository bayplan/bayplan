package main

import "testing"

func TestRedirectHost(t *testing.T) {
	cases := []struct {
		in   string
		acme []string
		want string
		ok   bool
	}{
		{"panel.example.com:8080", nil, "panel.example.com", true},
		{"10.0.0.5:8080", nil, "10.0.0.5", true},
		{"[::1]:8080", nil, "::1", true},
		{"localhost", nil, "localhost", true},
		{"evil.com/path", nil, "", false},
		{"user@evil.com", nil, "", false},
		{`evil.com\x`, nil, "", false},
		{"", nil, "", false},
		{"-bad.com", nil, "", false},
		{"panel.example.com", []string{"panel.example.com"}, "panel.example.com", true},
		{"other.example.com", []string{"panel.example.com"}, "", false},
		{"10.0.0.5", []string{"panel.example.com"}, "10.0.0.5", true},
	}
	for _, c := range cases {
		got, ok := redirectHost(c.in, c.acme)
		if got != c.want || ok != c.ok {
			t.Errorf("redirectHost(%q, %v) = %q, %v; expected %q, %v", c.in, c.acme, got, ok, c.want, c.ok)
		}
	}
}
