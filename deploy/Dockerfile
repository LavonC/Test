# Multi-stage Dockerfile for UniCompute Cloud Coordinator
FROM golang:1.22-alpine AS builder

WORKDIR /app

# Install build dependencies
RUN apk add --no-cache git ca-certificates

# Cache dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Compile static binary for Linux
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" -o /unicompute-coordinator ./cmd/unicompute-coordinator

# Minimal runtime image
FROM alpine:3.19

RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app
COPY --from=builder /unicompute-coordinator /app/unicompute-coordinator

# Expose HTTP/WSS signaling port (8080) and TURN relay UDP port (3478)
EXPOSE 8080/tcp
EXPOSE 3478/udp

# Environment defaults
ENV UNICOMPUTE_LISTEN="0.0.0.0:8080" \
    UNICOMPUTE_PUBLIC_IP="127.0.0.1" \
    UNICOMPUTE_REALM="unicompute.edu"

ENTRYPOINT ["/app/unicompute-coordinator"]
