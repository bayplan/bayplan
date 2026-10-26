package backup

import (
	"bytes"
	"context"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeS3 implements just enough of the S3 API (path-style) that the package
// uses: HeadBucket, PutObject, GetObject, DeleteObject and ListObjectsV2.
type fakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)
	bucket := parts[0]
	if bucket != "backups" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	key := ""
	if len(parts) == 2 {
		key = parts[1]
	}
	switch {
	case r.Method == http.MethodHead && key == "":
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodGet && key == "":
		prefix := r.URL.Query().Get("prefix")
		type obj struct {
			XMLName xml.Name `xml:"Contents"`
			Key     string   `xml:"Key"`
			Size    int      `xml:"Size"`
		}
		var out struct {
			XMLName     xml.Name `xml:"ListBucketResult"`
			Name        string   `xml:"Name"`
			IsTruncated bool     `xml:"IsTruncated"`
			KeyCount    int      `xml:"KeyCount"`
			Contents    []obj
		}
		out.Name = bucket
		var keys []string
		for k := range f.objects {
			if strings.HasPrefix(k, prefix) {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			out.Contents = append(out.Contents, obj{Key: k, Size: len(f.objects[k])})
		}
		out.KeyCount = len(keys)
		w.Header().Set("Content-Type", "application/xml")
		_ = xml.NewEncoder(w).Encode(out)
	case r.Method == http.MethodPut:
		b, _ := io.ReadAll(r.Body)
		f.objects[key] = b
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodGet:
		b, ok := f.objects[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(b)
	case r.Method == http.MethodDelete:
		delete(f.objects, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusBadRequest)
	}
}

func newFakeTarget(t *testing.T) (*Target, *fakeS3) {
	f := &fakeS3{objects: map[string][]byte{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return &Target{Name: "lab", Endpoint: srv.URL, Region: "us-east-1", Bucket: "backups", Prefix: "bayplan", AccessKeyID: "a", SecretAccessKey: "s", PathStyle: true}, f
}

func TestPackUnpackRoundTrip(t *testing.T) {
	items := []Item{
		{Kind: "deployments", Namespace: "app", Name: "api", YAML: "kind: Deployment\nmetadata:\n  name: api\n"},
		{Kind: "storageclasses", Name: "fast", YAML: "kind: StorageClass\nmetadata:\n  name: fast\n"},
	}
	data, err := Pack(Manifest{Cluster: "prod", ClusterID: 1, Schedule: "daily", CreatedAt: time.Now()}, items)
	if err != nil {
		t.Fatal(err)
	}
	m, files, err := Unpack(bytes.NewReader(data), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Items) != 2 || m.Items[0].Path != "app/deployments/api.yaml" || m.Items[1].Path != "_cluster/storageclasses/fast.yaml" {
		t.Fatalf("unexpected index: %+v", m.Items)
	}
	if files["app/deployments/api.yaml"] != items[0].YAML {
		t.Fatal("the YAML does not survive the round trip")
	}
	// Reading only the index loads no files.
	_, files, err = Unpack(bytes.NewReader(data), map[string]bool{})
	if err != nil || len(files) != 0 {
		t.Fatalf("index only: %v %d", err, len(files))
	}
}

func TestS3ValidateUploadPruneDownload(t *testing.T) {
	target, f := newFakeTarget(t)
	ctx := context.Background()
	if err := Validate(ctx, target); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if len(f.objects) != 0 {
		t.Fatal("the validation probe must be deleted")
	}
	folder := target.key("cluster-1", "daily")
	for i, ts := range []string{"20260101T000000Z", "20260102T000000Z", "20260103T000000Z"} {
		if err := upload(ctx, target, folder+"/"+ts+".tar.gz", []byte{byte(i)}); err != nil {
			t.Fatal(err)
		}
	}
	deleted, err := prune(ctx, target, folder, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 1 || !strings.HasSuffix(deleted[0], "20260101T000000Z.tar.gz") {
		t.Fatalf("retention must delete the oldest one: %v", deleted)
	}
	got, err := download(ctx, target, folder+"/20260103T000000Z.tar.gz")
	if err != nil || !bytes.Equal(got, []byte{2}) {
		t.Fatalf("download: %v %v", got, err)
	}
	bad := *target
	bad.Bucket = "other"
	if err := Validate(ctx, &bad); err == nil {
		t.Fatal("a nonexistent bucket must fail")
	}
}

func TestScheduleDue(t *testing.T) {
	now := time.Now()
	past := now.Add(-25 * time.Hour)
	recent := now.Add(-time.Hour)
	if !(Schedule{Enabled: true, IntervalHours: 24}).Due(now) {
		t.Fatal("with no previous run it is due")
	}
	if !(Schedule{Enabled: true, IntervalHours: 24, LastRunAt: &past}).Due(now) {
		t.Fatal("past the interval it is due")
	}
	if (Schedule{Enabled: true, IntervalHours: 24, LastRunAt: &recent}).Due(now) {
		t.Fatal("within the interval it is not due")
	}
	if (Schedule{Enabled: true, IntervalHours: 0}).Due(now) || (Schedule{Enabled: false, IntervalHours: 1}).Due(now) {
		t.Fatal("manual or disabled is never due")
	}
}
