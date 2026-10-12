# Reference business integration (WMS packing console). Talks to CPS over HTTP only.
# Build from this directory:  docker build -t business-demo business-demo/
ARG GO_IMAGE=golang:1.26-alpine
ARG RUNTIME_IMAGE=gcr.io/distroless/static-debian12:nonroot

FROM ${GO_IMAGE} AS build
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/business-demo .

FROM ${RUNTIME_IMAGE}
COPY --from=build /out/business-demo /usr/local/bin/business-demo
EXPOSE 8090
ENTRYPOINT ["/usr/local/bin/business-demo"]
