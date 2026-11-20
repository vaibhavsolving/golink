package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vaibhavsolving/golink/internal/metrics"
	"github.com/vaibhavsolving/golink/internal/ratelimit"
	"github.com/vaibhavsolving/golink/internal/store"
)

func newTestServer(rps float64, burst int) (*Server, *httptest.Server) {
	s := &Server{
		Store:   store.New(100),
		Limiter: ratelimit.New(rps, burst),
		Metrics: metrics.New(),
		Log:     slog.New(slog.NewJSONHandler(io.Discard, nil)),
		BaseURL: "http://sho.rt",
		Version: "test",
	}
	s.SetReady(true)
	return s, httptest.NewServer(s.Handler())
}

func noRedirect() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}

func create(t *testing.T, ts *httptest.Server, body string) (*http.Response, linkResp) {
	t.Helper()
	res, err := http.Post(ts.URL+"/api/v1/links", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var lr linkResp
	json.NewDecoder(res.Body).Decode(&lr)
	return res, lr
}

func TestCreateRedirectAndStats(t *testing.T) {
	_, ts := newTestServer(100, 100)
	defer ts.Close()

	res, lr := create(t, ts, `{"url":"https://github.com/vaibhavsolving"}`)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d", res.StatusCode)
	}
	if lr.ShortURL != "http://sho.rt/"+lr.Code {
		t.Fatalf("short_url = %q", lr.ShortURL)
	}

	r, err := noRedirect().Get(ts.URL + "/" + lr.Code)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusFound || r.Header.Get("Location") != "https://github.com/vaibhavsolving" {
		t.Fatalf("redirect status=%d location=%q", r.StatusCode, r.Header.Get("Location"))
	}

	r, _ = http.Get(ts.URL + "/api/v1/links/" + lr.Code)
	var stats linkResp
	json.NewDecoder(r.Body).Decode(&stats)
	r.Body.Close()
	if stats.Hits != 1 {
		t.Fatalf("hits = %d, want 1", stats.Hits)
	}
}

func TestValidation(t *testing.T) {
	_, ts := newTestServer(100, 100)
	defer ts.Close()
	for _, body := range []string{
		`not json`,
		`{"url":""}`,
		`{"url":"ftp://example.com"}`,
		`{"url":"javascript:alert(1)"}`,
		`{"url":"https://example.com","ttl_seconds":-5}`,
	} {
		res, _ := create(t, ts, body)
		if res.StatusCode != http.StatusBadRequest {
			t.Errorf("body %q: status = %d, want 400", body, res.StatusCode)
		}
	}
}

func TestNotFound(t *testing.T) {
	_, ts := newTestServer(100, 100)
	defer ts.Close()
	r, _ := http.Get(ts.URL + "/doesnotexist")
	r.Body.Close()
	if r.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d", r.StatusCode)
	}
}

func TestRateLimit(t *testing.T) {
	_, ts := newTestServer(0.001, 2)
	defer ts.Close()
	codes := []int{}
	for i := 0; i < 3; i++ {
		res, _ := create(t, ts, `{"url":"https://example.com"}`)
		codes = append(codes, res.StatusCode)
	}
	if codes[0] != 201 || codes[1] != 201 || codes[2] != 429 {
		t.Fatalf("codes = %v, want [201 201 429]", codes)
	}
}

func TestProbesAndMetrics(t *testing.T) {
	s, ts := newTestServer(100, 100)
	defer ts.Close()

	r, _ := http.Get(ts.URL + "/healthz")
	r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatalf("healthz = %d", r.StatusCode)
	}
	if r.Header.Get("X-Request-ID") == "" {
		t.Fatal("missing X-Request-ID header")
	}

	s.SetReady(false)
	r, _ = http.Get(ts.URL + "/readyz")
	r.Body.Close()
	if r.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("readyz while draining = %d, want 503", r.StatusCode)
	}

	r, _ = http.Get(ts.URL + "/metrics")
	body, _ := io.ReadAll(r.Body)
	r.Body.Close()
	out := string(body)
	for _, want := range []string{
		`golink_http_requests_total{method="GET",route="GET /healthz",status="200"} 1`,
		`golink_http_request_duration_seconds_count{route="GET /readyz"} 1`,
		"golink_uptime_seconds",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("metrics missing %q\n%s", want, out)
		}
	}
}
