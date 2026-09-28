# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.27.1-alpine@sha256:8a5910f31396cd4d89662f56c68b3ae31d374308270a1c3bd96672ee5ed43414 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download && go mod verify
COPY cmd/ ./cmd/
COPY internal/ ./internal/
ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/speedtest-api ./cmd/speedtest-api

FROM scratch
LABEL org.opencontainers.image.title="OneMind Services Speedtest API" \
      org.opencontainers.image.source="https://github.com/Onemind-Services-LLC/speedtest-api"
COPY --from=build /out/speedtest-api /speedtest-api
USER 65532:65532
EXPOSE 8080/tcp 8081/udp 8443/tcp 8082/tcp
ENTRYPOINT ["/speedtest-api"]
