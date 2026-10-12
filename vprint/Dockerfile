# Virtual Zebra-style network printer (vprint) and the standalone ZPL renderer (zplpng).
# Knows nothing about CPS: anything that speaks raw ZPL on :9100 can drive it.
# Build from the repo root:  docker build -f vprint/Dockerfile -t vprint .
ARG GO_IMAGE=golang:1.26-alpine
ARG RUNTIME_IMAGE=gcr.io/distroless/static-debian12:nonroot

FROM ${GO_IMAGE} AS build
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY pkg/ pkg/
COPY vprint/ vprint/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/vprint ./vprint/cmd/vprint && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/zplpng ./vprint/cmd/zplpng

FROM ${RUNTIME_IMAGE}
COPY --from=build /out/ /usr/local/bin/
# 9100: raw ZPL print port · 19200: gallery + control API
EXPOSE 9100 19200
ENTRYPOINT ["/usr/local/bin/vprint"]
CMD ["-listen", ":9100", "-admin", ":19200", "-render"]
