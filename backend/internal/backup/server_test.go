package backup

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bayplan/bayplan/internal/settings"
)

func TestLibpqEnv(t *testing.T) {
	env, err := LibpqEnv("postgres://bayplan:s3cr%40t@db.internal:5433/bayplan?sslmode=require")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"PGHOST=db.internal", "PGPORT=5433", "PGUSER=bayplan", "PGDATABASE=bayplan", "PGPASSWORD=s3cr@t", "PGSSLMODE=require"} {
		if !slices.Contains(env, want) {
			t.Errorf("missing %s in %v", want, env)
		}
	}

	// The embedded database: Unix socket, no password.
	env, err = LibpqEnv("postgres://bayplan@/bayplan?host=/tmp/bayplan-pg&sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(env, "PGHOST=/tmp/bayplan-pg") || slices.ContainsFunc(env, func(e string) bool { return strings.HasPrefix(e, "PGPASSWORD=") }) {
		t.Fatalf("socket URL: %v", env)
	}

	env, err = LibpqEnv("host=db user=u password=p dbname=d sslmode=verify-full")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(env, "PGSSLMODE=verify-full") {
		t.Fatalf("key=value DSN: %v", env)
	}
}

func TestServerBackupDue(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	c := settings.ServerBackup{Enabled: true, TargetID: 1, IntervalHours: 24, Keep: 7}
	if !Due(c, nil, now) {
		t.Fatal("never run: due")
	}
	last := now.Add(-23 * time.Hour)
	if Due(c, &last, now) {
		t.Fatal("23 h ago with a 24 h interval: not due")
	}
	last = now.Add(-24 * time.Hour)
	if !Due(c, &last, now) {
		t.Fatal("exactly one interval ago: due")
	}
	c.Enabled = false
	if Due(c, nil, now) {
		t.Fatal("disabled: never due")
	}
}

func TestValidateServerBackupSchedule(t *testing.T) {
	if err := ValidateSchedule(&settings.ServerBackup{}); err != nil {
		t.Fatalf("disabled needs nothing else: %v", err)
	}
	for _, c := range []settings.ServerBackup{
		{Enabled: true, IntervalHours: 24, Keep: 7},
		{Enabled: true, TargetID: 1, IntervalHours: 0, Keep: 7},
		{Enabled: true, TargetID: 1, IntervalHours: 24, Keep: 0},
	} {
		if err := ValidateSchedule(&c); err == nil {
			t.Errorf("%+v must be rejected", c)
		}
	}
}

// The server dumps share the bucket (and maybe the prefix) with the cluster
// backups: their retention must only ever touch .dump objects.
func TestServerDumpUploadAndRetention(t *testing.T) {
	target, f := newFakeTarget(t)
	ctx := context.Background()
	folder := target.key(ServerBackupFolder)
	if err := upload(ctx, target, folder+"/20260101T000000Z.tar.gz", []byte("cluster backup")); err != nil {
		t.Fatal(err)
	}
	for i, ts := range []string{"20260101T000000Z-1", "20260102T000000Z-2", "20260103T000000Z-3"} {
		data := []byte{byte(i), 'P', 'G'}
		if err := uploadFile(ctx, target, folder+"/"+ts+".dump", bytes.NewReader(data), int64(len(data))); err != nil {
			t.Fatal(err)
		}
	}
	deleted, err := pruneSuffix(ctx, target, folder, ".dump", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 1 || !strings.HasSuffix(deleted[0], "20260101T000000Z-1.dump") {
		t.Fatalf("retention must delete only the oldest dump: %v", deleted)
	}
	if _, ok := f.objects[folder+"/20260101T000000Z.tar.gz"]; !ok {
		t.Fatal("the dump retention deleted a cluster backup")
	}
	got, err := download(ctx, target, folder+"/20260103T000000Z-3.dump")
	if err != nil || !bytes.Equal(got, []byte{2, 'P', 'G'}) {
		t.Fatalf("download: %v %v", got, err)
	}
}
