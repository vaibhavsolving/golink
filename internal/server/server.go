// Package server wires the HTTP API, ops endpoints and middleware together.
package server

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/vaibhavsolving/golink/internal/metrics"
	"github.com/vaibhavsolving/golink/internal/ratelimit"
	"github.com/vaibhavsolving/golink/internal/store"
)

const maxURLLen = 2048

// Server holds dependencies for the HTTP handlers.
type Server struct {
	Store      *store.Store
	Limiter    *ratelimit.Limiter
	Metrics    *metrics.Registry
	Log        *slog.Logger
	BaseURL    string
	TrustProxy bool
	Version    string

	ready atomic.Bool
}

// SetReady flips the readiness probe. It is set false during shutdown so a
// load balancer or Kubernetes stops sending new traffic before we exit.
func (s *Server) SetReady(v bool) { s.ready.Store(v) }

// Handler returns the full HTTP handler with middleware applied.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Ops endpoints: no rate limiting.
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /readyz", s.readyz)
	mux.HandleFunc("GET /metrics", s.metrics)
	mux.HandleFunc("GET /version", s.version)

	// Public API.
	mux.Handle("POST /api/v1/links", s.rateLimit(http.HandlerFunc(s.createLink)))
	mux.Handle("GET /api/v1/links/{code}", s.rateLimit(http.HandlerFunc(s.getLink)))
	mux.Handle("GET /{code}", s.rateLimit(http.HandlerFunc(s.redirect)))

	return s.requestID(s.observe(mux))
}

type createReq struct {
	URL        string `json:"url"`
	TTLSeconds int    `json:"ttl_seconds"`
}

type linkResp struct {
	Code      string     `json:"code"`
	ShortURL  string     `json:"short_url"`
	URL       string     `json:"url"`
	Hits      uint64     `json:"hits"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

func (s *Server) toResp(l store.Link) linkResp {
	r := linkResp{
		Code:      l.Code,
		ShortURL:  strings.TrimRight(s.BaseURL, "/") + "/" + l.Code,
		URL:       l.URL,
		Hits:      l.Hits,
		CreatedAt: l.CreatedAt,
	}
	if !l.ExpiresAt.IsZero() {
		e := l.ExpiresAt
		r.ExpiresAt = &e
	}
	return r
}

func validURL(raw string) bool {
	if raw == "" || len(raw) > maxURLLen {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func (s *Server) createLink(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var req createReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if !validURL(req.URL) {
		writeErr(w, http.StatusBadRequest, "url must be an absolute http(s) URL under 2048 characters")
		return
	}
	if req.TTLSeconds < 0 {
		writeErr(w, http.StatusBadRequest, "ttl_seconds must be >= 0")
		return
	}
	l, err := s.Store.Create(req.URL, time.Duration(req.TTLSeconds)*time.Second)
	if err != nil {
		s.Log.Error("create link", "err", err)
		writeErr(w, http.StatusInternalServerError, "could not create link")
		return
	}
	writeJSON(w, http.StatusCreated, s.toResp(l))
}

func (s *Server) getLink(w http.ResponseWriter, r *http.Request) {
	l, err := s.Store.Get(r.PathValue("code"))
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.toResp(l))
}

func (s *Server) redirect(w http.ResponseWriter, r *http.Request) {
	l, err := s.Store.Resolve(r.PathValue("code"))
	if err != nil {
		writeStoreErr(w, err)
		return
	}
	http.Redirect(w, r, l.URL, http.StatusFound)
}

func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) readyz(w http.ResponseWriter, _ *http.Request) {
	if !s.ready.Load() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not ready"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (s *Server) metrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	s.Metrics.Write(w)
}

func (s *Server) version(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"version": s.Version})
}

func writeStoreErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeErr(w, http.StatusNotFound, "link not found")
	case errors.Is(err, store.ErrExpired):
		writeErr(w, http.StatusGone, "link expired")
	default:
		writeErr(w, http.StatusInternalServerError, "internal error")
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
