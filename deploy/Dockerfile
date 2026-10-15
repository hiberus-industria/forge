# syntax=docker/dockerfile:1
# Build context: repository root (docker build -f deploy/Dockerfile .)
FROM golang:1.26 AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOTOOLCHAIN=auto
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/forge-api ./cmd/forge-api

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/forge-api /forge-api
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/forge-api"]
