# golink

A production-style URL shortener in Go, built to practise the full DevOps lifecycle: containerisation, CI/CD, monitoring, alerting and graceful operations.

The service uses only the Go standard library (no third-party dependencies), so every piece (cache, rate limiter, metrics) is written and tested in this repo.

## Architecture

```
            ┌──────────────── docker compose ────────────────┐
 client ──► │ golink :8080 ──/metrics──► Prometheus :9090 ──► Grafana :3000
            │   │                          (alert rules)      │
            │   └── /app/data/links.json (named volume)       │
            └─────────────────────────────────────────────────┘

 GitHub push ─► Actions: gofmt ─ vet ─ staticcheck ─ test -race
                    └─► docker build ─ Trivy scan ─ smoke test ─ push to GHCR
```

## Features

| Area | What it does |
|---|---|
| Storage | In-memory store with **LRU eviction** and **per-link TTL**; thread-safe with a mutex |
| Persistence | Periodic **atomic snapshots** (temp file + rename) and a final save on shutdown; restored on start |
| Rate limiting | **Per-IP token bucket**; returns `429` with `Retry-After`; idle buckets cleaned up |
| Observability | **Prometheus** `/metrics` (request counter, latency histogram, stored-links gauge, uptime); **structured JSON logs** with request IDs |
| Health | `/healthz` (liveness) and `/readyz` (readiness, flips to 503 while draining) |
| Operations | **Graceful shutdown** on SIGTERM; 12-factor config via env vars; HTTP server timeouts |
| Container | **Multi-stage build** to a **distroless, non-root** image; built-in `HEALTHCHECK` |
| CI/CD | GitHub Actions: lint, race-detector tests, coverage, image build, **Trivy** vulnerability scan, container smoke test, push to **GHCR** on `main` |

## API

```bash
# Create (ttl_seconds optional, 0 = never expires)
curl -XPOST localhost:8080/api/v1/links -d '{"url":"https://github.com","ttl_seconds":3600}'

# Redirect
curl -i localhost:8080/<code>

# Stats
curl localhost:8080/api/v1/links/<code>

# Ops
curl localhost:8080/healthz
curl localhost:8080/readyz
curl localhost:8080/metrics
curl localhost:8080/version
```

## Run it

```bash
make run        # local, needs Go 1.23+
make test       # unit tests with -race
make bench      # store benchmark
make up         # app + Prometheus + Grafana in Docker
make load       # send 200 requests and watch the dashboard + 429s
make down
```

Grafana: http://localhost:3000 (admin / admin), dashboard **golink → golink service**.

## Configuration

| Variable | Default | Meaning |
|---|---|---|
| `PORT` | `8080` | Listen port |
| `BASE_URL` | `http://localhost:$PORT` | Prefix for returned short URLs |
| `DATA_FILE` | `data/links.json` | Snapshot path |
| `CAPACITY` | `100000` | Max links before LRU eviction |
| `SNAPSHOT_INTERVAL` | `30s` | How often to snapshot and purge expired links |
| `RATE_LIMIT_RPS` | `5` | Token refill rate per client IP |
| `RATE_LIMIT_BURST` | `20` | Bucket size per client IP |
| `TRUST_PROXY` | `false` | Use `X-Forwarded-For` for client IP (only behind a trusted proxy) |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |

## Deploy on a cloud VM (AWS EC2 / GCP Compute Engine)

```bash
docker run -d --name golink --restart unless-stopped \
  -p 80:8080 -v golink-data:/app/data \
  -e BASE_URL=http://<your-ip> \
  ghcr.io/vaibhavsolving/golink:latest
```

## Design notes

- **Why label metrics by route pattern, not path?** Every short code is a unique path. Labelling by path would create a new time series per link and blow up Prometheus memory (high cardinality). `GET /{code}` keeps it to one series.
- **Why atomic snapshots?** Writing straight to `links.json` and crashing halfway leaves a corrupt file. Writing to a temp file, fsyncing, then renaming is atomic on the same filesystem.
- **Why readiness separate from liveness?** During shutdown the process is alive but should get no new traffic. `/readyz` returns 503 so a load balancer drains it while in-flight requests finish.
- **Why distroless?** No shell or package manager means a much smaller attack surface and fewer CVEs for Trivy to flag. The binary handles its own health check because there is no `curl` in the image.
- **Why trust `X-Forwarded-For` only behind a proxy?** Otherwise any client can set that header and bypass the rate limiter.
