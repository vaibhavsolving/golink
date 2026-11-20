// Package metrics exposes request counters and latency histograms in the
// Prometheus text exposition format, using only the standard library.
package metrics

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
)

var defaultBuckets = []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1}

type reqKey struct {
	method, route string
	status        int
}

type histogram struct {
	counts []uint64 // one per bucket, cumulative is computed on export
	sum    float64
	count  uint64
}

// Registry collects HTTP metrics plus any gauges registered by the app.
type Registry struct {
	mu        sync.Mutex
	requests  map[reqKey]uint64
	latencies map[string]*histogram // by route
	gauges    map[string]func() float64
	help      map[string]string
	start     time.Time
}

func New() *Registry {
	return &Registry{
		requests:  make(map[reqKey]uint64),
		latencies: make(map[string]*histogram),
		gauges:    make(map[string]func() float64),
		help:      make(map[string]string),
		start:     time.Now(),
	}
}

// ObserveRequest records one finished HTTP request.
func (r *Registry) ObserveRequest(method, route string, status int, d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests[reqKey{method, route, status}]++

	h, ok := r.latencies[route]
	if !ok {
		h = &histogram{counts: make([]uint64, len(defaultBuckets))}
		r.latencies[route] = h
	}
	sec := d.Seconds()
	for i, b := range defaultBuckets {
		if sec <= b {
			h.counts[i]++
			break
		}
	}
	h.sum += sec
	h.count++
}

// RegisterGauge adds a gauge whose value is read at scrape time.
func (r *Registry) RegisterGauge(name, help string, fn func() float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gauges[name] = fn
	r.help[name] = help
}

// Write writes all metrics in Prometheus text format.
func (r *Registry) Write(w io.Writer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var b strings.Builder

	b.WriteString("# HELP golink_http_requests_total Total HTTP requests by method, route and status.\n")
	b.WriteString("# TYPE golink_http_requests_total counter\n")
	keys := make([]reqKey, 0, len(r.requests))
	for k := range r.requests {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].route != keys[j].route {
			return keys[i].route < keys[j].route
		}
		if keys[i].method != keys[j].method {
			return keys[i].method < keys[j].method
		}
		return keys[i].status < keys[j].status
	})
	for _, k := range keys {
		fmt.Fprintf(&b, "golink_http_requests_total{method=%q,route=%q,status=\"%d\"} %d\n",
			k.method, k.route, k.status, r.requests[k])
	}

	b.WriteString("# HELP golink_http_request_duration_seconds HTTP request latency by route.\n")
	b.WriteString("# TYPE golink_http_request_duration_seconds histogram\n")
	routes := make([]string, 0, len(r.latencies))
	for k := range r.latencies {
		routes = append(routes, k)
	}
	sort.Strings(routes)
	for _, route := range routes {
		h := r.latencies[route]
		var cum uint64
		for i, le := range defaultBuckets {
			cum += h.counts[i]
			fmt.Fprintf(&b, "golink_http_request_duration_seconds_bucket{route=%q,le=\"%g\"} %d\n", route, le, cum)
		}
		fmt.Fprintf(&b, "golink_http_request_duration_seconds_bucket{route=%q,le=\"+Inf\"} %d\n", route, h.count)
		fmt.Fprintf(&b, "golink_http_request_duration_seconds_sum{route=%q} %g\n", route, h.sum)
		fmt.Fprintf(&b, "golink_http_request_duration_seconds_count{route=%q} %d\n", route, h.count)
	}

	names := make([]string, 0, len(r.gauges))
	for k := range r.gauges {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s gauge\n%s %g\n", n, r.help[n], n, n, r.gauges[n]())
	}

	fmt.Fprintf(&b, "# HELP golink_uptime_seconds Seconds since the process started.\n# TYPE golink_uptime_seconds gauge\ngolink_uptime_seconds %g\n",
		time.Since(r.start).Seconds())

	io.WriteString(w, b.String())
}
