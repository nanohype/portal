// Package metrics is portal's Prometheus instrumentation. The deployment cluster
// runs a Grafana stack (Grafana Agent in flow mode → Amazon Managed Prometheus
// for metrics, Loki for logs, Tempo for traces). The agent scrapes pods carrying
// the prometheus.io/scrape annotation, so server and worker each expose /metrics
// and register the same metric set here.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const namespace = "portal"

// HTTP RED — rate, errors, duration are all derivable from this histogram by
// route + status, plus an in-flight gauge.
var (
	httpDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "http", Name: "request_duration_seconds",
		Help:    "HTTP request duration by method, route pattern, and status.",
		Buckets: []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
	}, []string{"method", "route", "status"})

	httpInFlight = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "http", Name: "requests_in_flight",
		Help: "In-flight HTTP requests.",
	})

	// tofu/terragrunt run wall-clock by operation (plan/apply/destroy/...) and
	// final status — the core "is infra execution healthy and how long" signal.
	tofuRunDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "tofu", Name: "run_duration_seconds",
		Help:    "tofu/terragrunt run duration by operation and final status.",
		Buckets: []float64{1, 5, 15, 30, 60, 120, 300, 600, 1200, 1800, 3600},
	}, []string{"operation", "status"})

	// River job errors/panics by job kind — pairs with the ErrorHandler so a
	// silently-retrying job is visible as a climbing counter.
	jobErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Subsystem: "worker", Name: "job_errors_total",
		Help: "River job errors and panics by kind and event (error|panic).",
	}, []string{"kind", "event"})

	// River jobs by state, sampled periodically — queue depth + backlog + the
	// retryable/discarded states that signal trouble.
	jobsByState = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "worker", Name: "jobs",
		Help: "River jobs by state, sampled.",
	}, []string{"state"})

	// Per-watcher-loop liveness: last successful tick time + tick duration. A
	// loop that stalls shows as a frozen timestamp; one that overruns its
	// interval shows in the duration histogram.
	watcherLastTick = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace, Subsystem: "watcher", Name: "last_tick_timestamp_seconds",
		Help: "Unix time of the last successful tick per watcher loop.",
	}, []string{"loop"})

	watcherTickDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace, Subsystem: "watcher", Name: "tick_duration_seconds",
		Help:    "Watcher loop tick duration.",
		Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 30, 60, 120},
	}, []string{"loop"})
)

// Register builds a fresh registry with portal's metrics plus the standard Go +
// process collectors. Call once per process (server or worker).
func Register() *prometheus.Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		httpDuration, httpInFlight, tofuRunDuration,
		jobErrors, jobsByState, watcherLastTick, watcherTickDuration,
	)
	return reg
}

// Handler serves /metrics for the given registry.
func Handler(reg *prometheus.Registry) http.Handler {
	return promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
}

// Middleware records HTTP RED. Mount it inside the chi router so the route
// pattern (not the raw path) is the label — bounded cardinality.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpInFlight.Inc()
		defer httpInFlight.Dec()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		start := time.Now()
		next.ServeHTTP(ww, r)
		route := chi.RouteContext(r.Context()).RoutePattern()
		if route == "" {
			route = "unmatched"
		}
		httpDuration.WithLabelValues(r.Method, route, strconv.Itoa(ww.Status())).
			Observe(time.Since(start).Seconds())
	})
}

// RegisterPool exposes pgxpool connection-pool stats (the DB-saturation signal).
func RegisterPool(reg *prometheus.Registry, pool *pgxpool.Pool) {
	reg.MustRegister(&poolCollector{pool: pool})
}

// ── worker-side recorders ────────────────────────────────────────────────────

// ObserveTofuRun records a finished tofu/terragrunt run.
func ObserveTofuRun(operation, status string, d time.Duration) {
	tofuRunDuration.WithLabelValues(operation, status).Observe(d.Seconds())
}

// IncJobError increments the job-error counter (event is "error" or "panic").
func IncJobError(kind, event string) {
	jobErrors.WithLabelValues(kind, event).Inc()
}

// SetJobsByState publishes the sampled River job-state counts.
func SetJobsByState(counts map[string]int) {
	for state, n := range counts {
		jobsByState.WithLabelValues(state).Set(float64(n))
	}
}

// WatcherTick records a watcher loop's successful tick.
func WatcherTick(loop string, d time.Duration) {
	watcherLastTick.WithLabelValues(loop).Set(float64(time.Now().Unix()))
	watcherTickDuration.WithLabelValues(loop).Observe(d.Seconds())
}

// ── pgxpool collector ────────────────────────────────────────────────────────

type poolCollector struct{ pool *pgxpool.Pool }

var (
	poolTotal    = prometheus.NewDesc(namespace+"_db_pool_connections_total", "Total connections in the pgx pool.", nil, nil)
	poolIdle     = prometheus.NewDesc(namespace+"_db_pool_connections_idle", "Idle connections in the pgx pool.", nil, nil)
	poolAcquired = prometheus.NewDesc(namespace+"_db_pool_connections_acquired", "Currently acquired connections.", nil, nil)
	poolMax      = prometheus.NewDesc(namespace+"_db_pool_connections_max", "Maximum pool size.", nil, nil)
	poolWait     = prometheus.NewDesc(namespace+"_db_pool_acquire_wait_total", "Cumulative connection-acquire waits.", nil, nil)
)

func (c *poolCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- poolTotal
	ch <- poolIdle
	ch <- poolAcquired
	ch <- poolMax
	ch <- poolWait
}

func (c *poolCollector) Collect(ch chan<- prometheus.Metric) {
	s := c.pool.Stat()
	ch <- prometheus.MustNewConstMetric(poolTotal, prometheus.GaugeValue, float64(s.TotalConns()))
	ch <- prometheus.MustNewConstMetric(poolIdle, prometheus.GaugeValue, float64(s.IdleConns()))
	ch <- prometheus.MustNewConstMetric(poolAcquired, prometheus.GaugeValue, float64(s.AcquiredConns()))
	ch <- prometheus.MustNewConstMetric(poolMax, prometheus.GaugeValue, float64(s.MaxConns()))
	ch <- prometheus.MustNewConstMetric(poolWait, prometheus.CounterValue, float64(s.EmptyAcquireCount()))
}
