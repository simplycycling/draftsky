# ---- Builder ----
FROM golang:1.26.1-bookworm AS builder

WORKDIR /build

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o draftsky ./cmd/server

# ---- Final ----
FROM debian:bookworm-slim

RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

COPY --from=builder /build/draftsky ./
COPY templates/ ./templates/
COPY static/ ./static/

EXPOSE 8080

CMD ["/app/draftsky"]
