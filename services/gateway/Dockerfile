# syntax=docker/dockerfile:1
# Multi-stage gateway image. JWT secrets are NOT baked in — inject at runtime via compose.
FROM golang:1.24-bookworm AS build
WORKDIR /src
ENV CGO_ENABLED=0
ENV GOTOOLCHAIN=auto
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -trimpath -ldflags="-s -w" -o /out/gateway ./cmd/gateway

FROM alpine:3.21
RUN apk add --no-cache ca-certificates wget \
	&& adduser -D -H -u 65532 nonroot
COPY --from=build /out/gateway /usr/local/bin/gateway
USER nonroot
EXPOSE 18080
# Prefer /readyz when Forge lands it; fall back to /healthz.
HEALTHCHECK --interval=5s --timeout=3s --start-period=10s --retries=10 \
	CMD wget -qO- http://127.0.0.1:18080/readyz >/dev/null 2>&1 \
		|| wget -qO- http://127.0.0.1:18080/healthz >/dev/null 2>&1 \
		|| exit 1
ENTRYPOINT ["/usr/local/bin/gateway"]
