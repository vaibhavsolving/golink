# syntax=docker/dockerfile:1

# ---- Build stage ----
FROM golang:1.24-alpine AS build
WORKDIR /src

# Cache module downloads separately from source changes.
COPY go.mod ./
RUN go mod download

COPY . .
ARG VERSION=dev
# CGO off + stripped symbols = small, fully static binary.
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/golink ./cmd/golink

# Distroless has no shell or package manager, so it needs its own data dir.
RUN mkdir -p /out/data

# ---- Runtime stage ----
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/golink /app/golink
COPY --from=build --chown=nonroot:nonroot /out/data /app/data

ENV PORT=8080 \
    DATA_FILE=/app/data/links.json
EXPOSE 8080
USER nonroot:nonroot

# No curl in distroless: the binary probes itself.
HEALTHCHECK --interval=15s --timeout=3s --start-period=5s --retries=3 \
    CMD ["/app/golink", "-healthcheck"]

ENTRYPOINT ["/app/golink"]
