# GoLink: setup and interview prep (for Vaibhav)

The CV says you built this, so before it goes out the repo has to be live on your GitHub and you need to be able to explain every line. Read the code once end to end; it is about 900 lines.

## 1. Push it to GitHub (15 min)

1. On github.com create an empty **public** repo called `golink` under `vaibhavsolving` (no README, no .gitignore).
2. Unzip this folder, then:
   ```bash
   cd golink
   git init
   git add .
   git commit -m "GoLink: URL shortener with Docker, CI/CD and monitoring"
   git branch -M main
   git remote add origin https://github.com/vaibhavsolving/golink.git
   git push -u origin main
   ```
3. Open the **Actions** tab. Both jobs (Lint and test, Build/scan/publish image) should go green. If Trivy fails on a new CVE, bump the `golang:` base image tag in the Dockerfile and push again.
4. After the first green run, open **Packages** on your profile, open `golink`, and set its visibility to **Public** so recruiters can see the image.
5. Check that the GitHub link on the CV opens the repo.

## 2. Run it yourself at least once (20 min)

Install Go 1.23+ and Docker Desktop, then:
```bash
make test     # all tests pass
make up       # app + Prometheus + Grafana
make load     # see 201s turn into 429s once the rate limit kicks in
```
Open Grafana at localhost:3000 (admin/admin), go to golink → golink service, and watch the panels move. Take a screenshot and add it to the README; it makes the repo look much more credible.

Optional: run `make bench` and note the ns/op figure on your laptop. If you want a performance number on the CV, use your own measurement.

## 3. Questions you should be able to answer

**Why multi-stage Docker build?**
The build stage needs the Go toolchain (~250 MB). The runtime only needs the static binary, so the final image is the binary on distroless, a few MB.

**What is distroless, and why non-root?**
An image with no shell or package manager, so there is less to attack and fewer CVEs. Non-root means a container escape doesn't give root.

**How does the health check work without curl?**
`golink -healthcheck` calls `/healthz` on itself and exits 0 or 1. Docker runs it every 15 seconds.

**Liveness vs readiness?**
Liveness (`/healthz`) asks whether the process is alive; if not, restart it. Readiness (`/readyz`) asks whether it should get traffic. On SIGTERM readiness goes to 503 so the load balancer stops routing, in-flight requests finish within 15s, then the final snapshot is saved.

**What happens on `docker stop`?**
Docker sends SIGTERM, then SIGKILL after the grace period. `stop_grace_period: 20s` in compose is longer than the 15s shutdown timeout, so the app always finishes first.

**How does the token bucket work?**
Each IP has a bucket of `burst` tokens that refills at `rps` tokens per second. Each request spends one token, and an empty bucket returns 429. Bursts are allowed but the average rate is capped.

**Why not trust X-Forwarded-For by default?**
Any client can set that header, which would let them dodge the rate limit. Only trust it behind your own proxy.

**LRU + TTL?**
A hashmap plus a doubly linked list. Every read moves the item to the front; when over capacity, evict from the back. TTL is checked on read and by a periodic purge.

**Why temp file + rename for snapshots?**
Rename is atomic on the same filesystem, so a crash leaves either the old file or the new one, never a half-written file.

**Why are metrics labelled by route and not path?**
Every short code is a unique path, so labelling by path creates a new time series per link (cardinality explosion). The route pattern `GET /{code}` is one series.

**Walk me through the pipeline.**
On every push and PR: gofmt, vet, staticcheck, then tests with `-race`. Then the image is built with a GitHub Actions layer cache, Trivy fails the build on fixable HIGH/CRITICAL CVEs, and a smoke test starts the container and checks the redirect. On `main` only, the image is pushed to GHCR tagged with the commit SHA and `latest`.

**What would you add next?**
A Kubernetes Deployment with these probes, Redis or Postgres for shared state so it can scale past one replica (the in-memory store is single-instance by design), a Terraform-provisioned VM, and a deploy job after the push.

**Honest limitation to mention:**
The rate limiter and store are per-instance, so running two replicas would give each its own state. That is why the next step is moving state to Redis, which links back nicely to your cache library work at IndiaMART.
