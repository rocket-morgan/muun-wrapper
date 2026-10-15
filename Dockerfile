# syntax=docker/dockerfile:1.7

# Apollo base image tag printed by scripts/apollo-image.sh (see Dockerfile.apollo).
ARG APOLLO_IMAGE

FROM ${APOLLO_IMAGE} AS apollo

FROM golang:1.24-bookworm AS builder

COPY --from=apollo /deps/apollo/libwallet /deps/apollo/libwallet

WORKDIR /src
COPY . .

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod edit -replace github.com/muun/libwallet=/deps/apollo/libwallet \
    && CGO_ENABLED=1 go build -mod=mod -trimpath -ldflags='-s -w' -o /out/muun-wrapper ./cmd/muun-wrapper

FROM debian:bookworm-slim AS runtime

RUN apt-get update \
    && apt-get install --yes --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --create-home --uid 10001 --shell /usr/sbin/nologin muun

COPY --from=builder /out/muun-wrapper /usr/local/bin/muun-wrapper

USER muun
VOLUME ["/data"]
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/muun-wrapper"]
CMD ["server"]
