# Cloud Print Service: the API + embedded operator panel (cps) and the admin CLI (cpsctl).
# Build from the repo root:  docker build -f server/Dockerfile -t cps .
ARG GO_IMAGE=golang:1.26-alpine
ARG RUNTIME_IMAGE=alpine:3.22

FROM ${GO_IMAGE} AS build
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY pkg/ pkg/
COPY server/ server/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/cps    ./server/cmd/cps && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/cpsctl ./server/cmd/cpsctl

FROM ${RUNTIME_IMAGE}
# busybox wget is used by the container healthcheck; ca-certificates for labelize over TLS.
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 cps
COPY --from=build /out/ /usr/local/bin/
COPY server/config.example.json /etc/cps/config.json
USER cps
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=3s --retries=5 \
  CMD wget -q -O /dev/null http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["/usr/local/bin/cps"]
CMD ["-config", "/etc/cps/config.json"]
