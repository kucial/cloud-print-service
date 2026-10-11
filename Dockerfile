# syntax=docker/dockerfile:1
# One image, three entrypoints: the service, the admin CLI and the virtual printer.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/cps    ./cmd/cps && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/cpsctl ./cmd/cpsctl && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/vprint ./cmd/vprint && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/zplpng ./cmd/zplpng && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/demo-web ./cmd/demo-web

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/ /usr/local/bin/
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/cps"]
