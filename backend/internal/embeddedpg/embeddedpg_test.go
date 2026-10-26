package embeddedpg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseMajor(t *testing.T) {
	for in, want := range map[string]string{
		"postgres (PostgreSQL) 18.6\n":           "18",
		"postgres (PostgreSQL) 17.11":            "17",
		"postgres (PostgreSQL) 16.4 (Debian 16)": "16",
	} {
		got, err := parseMajor(in)
		if err != nil || got != want {
			t.Errorf("parseMajor(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := parseMajor("garbage"); err == nil {
		t.Error("garbage must be an error")
	}
}

func TestCheckMajor(t *testing.T) {
	if err := checkMajor("18", "18"); err != nil {
		t.Fatal(err)
	}
	if err := checkMajor("17", "18"); err == nil || !strings.Contains(err.Error(), "Upgrading the embedded PostgreSQL") {
		t.Fatalf("an older data directory must point to the upgrade procedure, got %v", err)
	}
	if err := checkMajor("19", "18"); err == nil || !strings.Contains(err.Error(), "downgrades") {
		t.Fatalf("a newer data directory must say downgrades are not supported, got %v", err)
	}
}

func TestURLIsSocketOnly(t *testing.T) {
	u := URL()
	if !strings.Contains(u, "@/bayplan?host=/") {
		t.Fatalf("the embedded database must be reached through the Unix socket, got %s", u)
	}
}

func TestEnsurePrivateDir(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "sock")
	if err := ensurePrivateDir(dir); err != nil {
		t.Fatalf("a fresh directory must be accepted: %v", err)
	}
	if err := ensurePrivateDir(dir); err != nil {
		t.Fatalf("our own 0700 directory must be accepted again: %v", err)
	}
	open := filepath.Join(base, "open")
	if err := os.Mkdir(open, 0o777); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(open, 0o777)
	if err := ensurePrivateDir(open); err == nil {
		t.Fatal("a directory others can write to must be refused")
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivateDir(link); err == nil {
		t.Fatal("a symlink must be refused")
	}
}
