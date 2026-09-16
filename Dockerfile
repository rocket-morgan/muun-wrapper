# syntax=docker/dockerfile:1.7

FROM rust:1.84 AS librs-builder

ARG APOLLO_COMMIT=e5f5e1bb54f611ef457c56207f2a34ac85b00827
ARG RUSTUP_TOOLCHAIN=nightly-2024-12-16
ARG LIBRS_TARGET=x86_64-unknown-linux-musl

RUN apt-get update \
    && apt-get install --yes --no-install-recommends ca-certificates git \
    && rm -rf /var/lib/apt/lists/* \
    && git clone https://github.com/muun/apollo.git /deps/apollo \
    && git -C /deps/apollo checkout --detach "$APOLLO_COMMIT" \
    && rustup toolchain install "$RUSTUP_TOOLCHAIN" \
    && rustup override set "$RUSTUP_TOOLCHAIN" \
    && rustup target add --toolchain "$RUSTUP_TOOLCHAIN" "$LIBRS_TARGET"

WORKDIR /deps/apollo

RUN TARGETS="$LIBRS_TARGET" USE_HOST_CARGO=true libwallet/librs/makelibs.sh

FROM golang:1.24-bookworm AS builder

COPY --from=librs-builder /deps/apollo /deps/apollo

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
