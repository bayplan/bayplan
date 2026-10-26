package api

import "testing"

func TestCSVSafeCell(t *testing.T) {
	cases := map[string]string{
		"":                     "",
		"admin":                "admin",
		"=HYPERLINK(\"x\")":    "'=HYPERLINK(\"x\")",
		"+1":                   "'+1",
		"-2+3":                 "'-2+3",
		"@SUM(A1)":             "'@SUM(A1)",
		"\tx":                  "'\tx",
		"\rx":                  "'\rx",
		"2026-10-19T00:00:00Z": "2026-10-19T00:00:00Z",
		`{"reason":"=cmd"}`:    `{"reason":"=cmd"}`,
		"login.local":          "login.local",
	}
	for in, want := range cases {
		if got := csvSafeCell(in); got != want {
			t.Errorf("csvSafeCell(%q) = %q, want %q", in, got, want)
		}
	}
}
