VERSION ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)

.PHONY: run test bench lint build docker up down logs load

run:            ## Run locally on :8080
	go run ./cmd/golink

test:           ## Unit tests with race detector
	go test -race -cover ./...

bench:          ## Store benchmark
	go test -run=^$$ -bench=. ./internal/store/

lint:           ## Format + vet
	gofmt -l . && go vet ./...

build:          ## Static binary in ./bin
	CGO_ENABLED=0 go build -ldflags "-s -w -X main.version=$(VERSION)" -o bin/golink ./cmd/golink

docker:         ## Build the container image
	docker build --build-arg VERSION=$(VERSION) -t golink:$(VERSION) .

up:             ## Start app + Prometheus + Grafana
	docker compose up -d --build

down:
	docker compose down

logs:
	docker compose logs -f golink

load:           ## Fire 200 create requests to see metrics and rate limiting move
	@for i in $$(seq 1 200); do \
	  curl -s -o /dev/null -w "%{http_code}\n" -XPOST localhost:8080/api/v1/links -d '{"url":"https://example.com"}'; \
	done | sort | uniq -c
