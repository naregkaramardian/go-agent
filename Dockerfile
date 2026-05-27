# ── Build stage ────────────────────────────────────────────────────────────────
FROM golang:1.25-alpine AS builder

WORKDIR /app

# Download modules before copying source so this layer is cached independently.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -trimpath -o goagent .

# ── Runtime stage ──────────────────────────────────────────────────────────────
FROM alpine:3.21

# ca-certificates: required for TLS connections to LLM APIs.
RUN apk add --no-cache ca-certificates

WORKDIR /app
COPY --from=builder /app/goagent .

ENTRYPOINT ["./goagent"]
CMD ["run"]
