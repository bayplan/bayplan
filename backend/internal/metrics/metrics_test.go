package metrics

import (
	"bufio"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bayplan/bayplan/internal/k8s"
)

type fakeClusterSource struct {
	readiness []k8s.ClusterReadiness
}

func (f fakeClusterSource) ClusterReadiness() []k8s.ClusterReadiness { return f.readiness }

type fakeLogsSource struct{ active int64 }

func (f fakeLogsSource) ActiveStreams() int64 { return f.active }

type fakeExecSource struct{ active int64 }

func (f fakeExecSource) ActiveExecs() int64 { return f.active }

func TestInstrumentHTTPRecordsRequests(t *testing.T) {
	m := New(fakeClusterSource{}, fakeLogsSource{}, fakeExecSource{})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/clusters/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	instrumented := m.InstrumentHTTP(mux)

	req := httptest.NewRequest(http.MethodGet, "/api/clusters/1", nil)
	rec := httptest.NewRecorder()
	instrumented.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	metricsRec := httptest.NewRecorder()
	m.Handler().ServeHTTP(metricsRec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := metricsRec.Body.String()

	if !strings.Contains(body, `bayplan_http_requests_total{method="GET",route="GET /api/clusters/{id}",status="200"}`) {
		t.Errorf("expected counter not found in the metrics exposition:\n%s", body)
	}
}

// hijackableRecorder is a test ResponseWriter that also implements
// http.Hijacker, to check that InstrumentHTTP delegates Hijack instead of
// breaking it (critical: the WS logs endpoint depends on this).
type hijackableRecorder struct {
	*httptest.ResponseRecorder
	hijacked bool
}

func (h *hijackableRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h.hijacked = true
	server, _ := net.Pipe()
	return server, bufio.NewReadWriter(bufio.NewReader(server), bufio.NewWriter(server)), nil
}

func TestInstrumentHTTPDelegatesHijack(t *testing.T) {
	m := New(fakeClusterSource{}, fakeLogsSource{}, fakeExecSource{})

	var gotHijacker bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, ok := w.(http.Hijacker)
		gotHijacker = ok
		if ok {
			hj := w.(http.Hijacker)
			conn, _, err := hj.Hijack()
			if err != nil {
				t.Fatalf("Hijack: %v", err)
			}
			conn.Close()
		}
	})

	instrumented := m.InstrumentHTTP(handler)

	rec := &hijackableRecorder{ResponseRecorder: httptest.NewRecorder()}
	req := httptest.NewRequest(http.MethodGet, "/ws/clusters/1/namespaces/default/pods/x/logs", nil)
	instrumented.ServeHTTP(rec, req)

	if !gotHijacker {
		t.Fatal("the inner handler did not see an http.Hijacker: InstrumentHTTP broke the wrapping")
	}
	if !rec.hijacked {
		t.Fatal("Hijack() was never invoked on the underlying recorder")
	}
}

func TestRecordLogin(t *testing.T) {
	m := New(fakeClusterSource{}, fakeLogsSource{}, fakeExecSource{})
	m.RecordLogin("success")
	m.RecordLogin("failure")
	m.RecordLogin("failure")

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()

	if !strings.Contains(body, `bayplan_login_total{result="success"} 1`) {
		t.Errorf("wrong login success counter:\n%s", body)
	}
	if !strings.Contains(body, `bayplan_login_total{result="failure"} 2`) {
		t.Errorf("wrong login failure counter:\n%s", body)
	}
}

func TestLiveCollectorGauges(t *testing.T) {
	clusters := fakeClusterSource{readiness: []k8s.ClusterReadiness{
		{Cluster: "PRE", InformerSynced: true},
		{Cluster: "PRO", InformerSynced: false, LastError: "network failure"},
	}}
	m := New(clusters, fakeLogsSource{active: 3}, fakeExecSource{active: 2})

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := rec.Body.String()

	if !strings.Contains(body, `bayplan_cluster_informer_synced{cluster="PRE"} 1`) {
		t.Errorf("wrong PRE gauge:\n%s", body)
	}
	if !strings.Contains(body, `bayplan_cluster_informer_synced{cluster="PRO"} 0`) {
		t.Errorf("wrong PRO gauge:\n%s", body)
	}
	if !strings.Contains(body, `bayplan_active_log_streams 3`) {
		t.Errorf("wrong active streams gauge:\n%s", body)
	}
	if !strings.Contains(body, `bayplan_active_exec_sessions 2`) {
		t.Errorf("wrong active exec sessions gauge:\n%s", body)
	}
}
