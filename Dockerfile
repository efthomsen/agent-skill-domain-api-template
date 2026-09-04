# ---- Stage 1: build the Go PocketBase application ----
FROM golang:1.25-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY main.go ./
COPY internal ./internal
COPY migrations ./migrations
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/agent-skill-domain-api-template .

# ---- Stage 2: runtime ----
FROM debian:bookworm-slim AS app
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /pb
COPY --from=build /out/agent-skill-domain-api-template /pb/agent-skill-domain-api-template

EXPOSE 8090
CMD ["/pb/agent-skill-domain-api-template", "serve", "--http=0.0.0.0:8090"]
