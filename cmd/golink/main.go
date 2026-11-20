// Command golink runs the URL shortener service.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/vaibhavsolving/golink/internal/metrics"
	"github.com/vaibhavsolving/golink/internal/ratelimit"
	"github.com/vaibhavsolving/golink/internal/server"
	"github.com/vaibhavsolving/golink/internal/store"
)

// version is set at build time: -ldflags "-X main.version=<git sha>"
var version = "dev"

type config struct {
	Port             string
	BaseURL          string
	DataFile         string
	Capacity         int
	SnapshotInterval time.Duration
	RateRPS          float64
	RateBurst        int
	TrustProxy       bool
	LogLevel         slog.Level
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func loadConfig() (config, error) {
	c := config{
		Port:     env("PORT", "8080"),
		DataFile: env("DATA_FILE", "data/links.json"),
	}
	c.BaseURL = env("BASE_URL", "http://localhost:"+c.Port)

	var err error
	if c.Capacity, err = strconv.Atoi(env("CAPACITY", "100000")); err != nil {
		return c, fmt.Errorf("CAPACITY: %w", err)
	}
	if c.SnapshotInterval, err = time.ParseDuration(env("SNAPSHOT_INTERVAL", "30s")); err != nil {
		return c, fmt.Errorf("SNAPSHOT_INTERVAL: %w", err)
	}
	if c.RateRPS, err = strconv.ParseFloat(env("RATE_LIMIT_RPS", "5"), 64); err != nil {
		return c, fmt.Errorf("RATE_LIMIT_RPS: %w", err)
	}
	if c.RateBurst, err = strconv.Atoi(env("RATE_LIMIT_BURST", "20")); err != nil {
		return c, fmt.Errorf("RATE_LIMIT_BURST: %w", err)
	}
	if c.TrustProxy, err = strconv.ParseBool(env("TRUST_PROXY", "false")); err != nil {
		return c, fmt.Errorf("TRUST_PROXY: %w", err)
	}
	if err = c.LogLevel.UnmarshalText([]byte(env("LOG_LEVEL", "info"))); err != nil {
		return c, fmt.Errorf("LOG_LEVEL: %w", err)
	}
	return c, nil
}

// healthcheck lets a distroless container (no shell, no curl) run its own
// Docker HEALTHCHECK: `golink -healthcheck`.
func healthcheck(port string) int {
	client := http.Client{Timeout: 2 * time.Second}
	res, err := client.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		return 1
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

func main() {
	check := flag.Bool("healthcheck", false, "probe /healthz on the local server and exit")
	flag.Parse()
	if *check {
		os.Exit(healthcheck(env("PORT", "8080")))
	}

	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config error:", err)
		os.Exit(1)
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))

	st := store.New(cfg.Capacity)
	n, err := st.Load(cfg.DataFile)
	if err != nil {
		log.Error("load snapshot", "file", cfg.DataFile, "err", err)
		os.Exit(1)
	}
	log.Info("snapshot loaded", "file", cfg.DataFile, "links", n)

	reg := metrics.New()
	reg.RegisterGauge("golink_links_stored", "Number of links currently stored.", func() float64 {
		return float64(st.Len())
	})

	srv := &server.Server{
		Store:      st,
		Limiter:    ratelimit.New(cfg.RateRPS, cfg.RateBurst),
		Metrics:    reg,
		Log:        log,
		BaseURL:    cfg.BaseURL,
		TrustProxy: cfg.TrustProxy,
		Version:    version,
	}

	httpSrv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Background maintenance: periodic snapshot, TTL purge, limiter cleanup.
	go func() {
		t := time.NewTicker(cfg.SnapshotInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				purged := st.PurgeExpired()
				srv.Limiter.Cleanup(10 * time.Minute)
				if err := st.Save(cfg.DataFile); err != nil {
					log.Error("snapshot failed", "err", err)
				} else {
					log.Debug("snapshot saved", "links", st.Len(), "purged", purged)
				}
			}
		}
	}()

	go func() {
		log.Info("server starting", "addr", httpSrv.Addr, "version", version)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server error", "err", err)
			stop()
		}
	}()
	srv.SetReady(true)

	<-ctx.Done()
	log.Info("shutdown signal received, draining connections")
	srv.SetReady(false)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Error("graceful shutdown failed", "err", err)
	}
	if err := st.Save(cfg.DataFile); err != nil {
		log.Error("final snapshot failed", "err", err)
		os.Exit(1)
	}
	log.Info("shutdown complete", "links_saved", st.Len())
}
