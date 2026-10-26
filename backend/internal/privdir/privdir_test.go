package privdir

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsure(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "work")
	if err := Ensure(dir); err != nil {
		t.Fatalf("fresh dir: %v", err)
	}
	if err := Ensure(dir); err != nil {
		t.Fatalf("existing own dir: %v", err)
	}

	open := filepath.Join(base, "open")
	if err := os.Mkdir(open, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Ensure(open); err == nil {
		t.Error("a directory readable by others must be refused")
	}

	link := filepath.Join(base, "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if err := Ensure(link); err == nil {
		t.Error("a symlink must be refused")
	}

	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Ensure(file); err == nil {
		t.Error("a file must be refused")
	}
}
