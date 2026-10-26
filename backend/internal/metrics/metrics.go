// Package metrics exposes /metrics in Prometheus format: HTTP requests
// (counter + duration per route/method/status), logins per result, and gauges
// read live on every scrape (informer sync per cluster, active log streams).
// No authentication, same as /healthz and /readyz: the trust is network-level
// (only reachable from the internal scraper), not session-level — Prometheus
// cannot do an interactive login.
package metrics

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/bayplan/bayplan/internal/k8s"
)

// ClusterReadinessSource is implemented by k8s.Manager.
type ClusterReadinessSource interface {
	ClusterReadiness() []k8s.ClusterReadiness
}

// LogStreamsSource is implemented by ws.LogsHandler.
type LogStreamsSource interface {
	ActiveStreams() int64
}

// ExecSessionsSource is implemented by ws.ExecHandler.
type ExecSessionsSource interface {
	ActiveExecs() int64
}

type Metrics struct {
	registry *prometheus.Registry

	httpRequestsTotal   *prometheus.CounterVec
	httpRequestDuration *prometheus.HistogramVec
	loginTotal          *prometheus.CounterVec
}

// New registers every metric and the live gauges (clusters, logs, exec) in a
// dedicated registry, not in the library's global one.
func New(clusters ClusterReadinessSource, logs LogStreamsSource, execs ExecSessionsSource) *Metrics {
	reg := prometheus.NewRegistry()

	m := &Metrics{
		registry: reg,
		httpRequestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "bayplan_http_requests_total",
			Help: "Total HTTP requests, by route, method and status code.",
		}, []string{"route", "method", "status"}),
		httpRequestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "bayplan_http_request_duration_seconds",
			Help:    "HTTP request duration, by route and method.",
			Buckets: prometheus.DefBuckets,
		}, []string{"route", "method"}),
		loginTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "bayplan_login_total",
			Help: "Login attempts, by result (success|failure).",
		}, []string{"result"}),
	}

	reg.MustRegister(m.httpRequestsTotal, m.httpRequestDuration, m.loginTotal)
	reg.MustRegister(&liveCollector{clusters: clusters, logs: logs, execs: execs})

	return m
}

// Handler serves the registry in the Prometheus exposition format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// RecordLogin counts a login attempt (local or SSO), by result.
func (m *Metrics) RecordLogin(result string) {
	m.loginTotal.WithLabelValues(result).Inc()
}

// InstrumentHTTP measures duration and counts requests by
// route+method+status. Meant to wrap the mux from the outside (including
// /metrics, /healthz and the static files). It must be mounted on the
// http.Server, not inside the mux itself, so that r.Pattern is already
// resolved by the ServeMux when it is read after next.ServeHTTP.
func (m *Metrics) InstrumentHTTP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		m.httpRequestsTotal.WithLabelValues(route, r.Method, strconv.Itoa(rec.status)).Inc()
		m.httpRequestDuration.WithLabelValues(route, r.Method).Observe(time.Since(start).Seconds())
	})
}

// statusRecorder captures the response status code without breaking
// Hijacker/Flusher: the WS logs endpoint depends on Hijack for the upgrade,
// and if this wrapper does not delegate it, log streaming breaks as soon as
// the whole server is instrumented.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("metrics: the underlying ResponseWriter does not support Hijack")
	}
	return hj.Hijack()
}

func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

var (
	clusterSyncedDesc = prometheus.NewDesc(
		"bayplan_cluster_informer_synced",
		"Whether the cluster's informer cache is synced (1) or not (0).",
		[]string{"cluster"}, nil,
	)
	activeLogStreamsDesc = prometheus.NewDesc(
		"bayplan_active_log_streams",
		"Pod log streams active right now.",
		nil, nil,
	)
	activeExecSessionsDesc = prometheus.NewDesc(
		"bayplan_active_exec_sessions",
		"Interactive terminal sessions (pods/exec) active right now.",
		nil, nil,
	)
)

// liveCollector recomputes its values on every scrape instead of keeping its
// own state: the gauges always reflect the real state of the process.
type liveCollector struct {
	clusters ClusterReadinessSource
	logs     LogStreamsSource
	execs    ExecSessionsSource
}

func (c *liveCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- clusterSyncedDesc
	ch <- activeLogStreamsDesc
	ch <- activeExecSessionsDesc
}

func (c *liveCollector) Collect(ch chan<- prometheus.Metric) {
	for _, cr := range c.clusters.ClusterReadiness() {
		v := 0.0
		if cr.InformerSynced {
			v = 1.0
		}
		ch <- prometheus.MustNewConstMetric(clusterSyncedDesc, prometheus.GaugeValue, v, cr.Cluster)
	}
	ch <- prometheus.MustNewConstMetric(activeLogStreamsDesc, prometheus.GaugeValue, float64(c.logs.ActiveStreams()))
	ch <- prometheus.MustNewConstMetric(activeExecSessionsDesc, prometheus.GaugeValue, float64(c.execs.ActiveExecs()))
}
