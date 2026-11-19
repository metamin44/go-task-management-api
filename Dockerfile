# Production-safe multi-stage build. The real .env is excluded via
# .dockerignore and must NEVER be baked into an image; all secrets are
# injected at runtime through environment variables.

FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/migrate ./cmd/migrate

FROM alpine:3.22
# ca-certificates for outbound TLS; wget (busybox) for the health check.
RUN apk add --no-cache ca-certificates \
 && adduser -D -u 10001 appuser
COPY --from=build /out/api /out/migrate /app/
USER appuser
WORKDIR /app
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8080/health || exit 1
ENTRYPOINT ["/app/api"]