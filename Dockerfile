# syntax=docker/dockerfile:1

# ---- Build ----
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY . .
# tidy fills in any go.sum entries that are missing, then build a static binary.
ARG VERSION=dev
RUN go mod tidy && \
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/blockheads ./cmd/blockheads

# ---- Run ----
# Debian rather than Alpine: Mojang's Bedrock server needs the standard GNU C
# library and libcurl, which Alpine doesn't have.
FROM debian:bookworm-slim
RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates tzdata libcurl4 libcap2-bin && \
    rm -rf /var/lib/apt/lists/* && \
    mkdir -p /config /data && chown 99:100 /config /data
COPY --from=build /out/blockheads /usr/local/bin/blockheads
# Lets the panel use port 53 (DNS) without running as root.
RUN setcap cap_net_bind_service=+ep /usr/local/bin/blockheads
# 99:100 is nobody:users, the owner Unraid uses for appdata.
USER 99:100
ENV HOME=/data
VOLUME ["/config", "/data"]
# 8443/tcp = web panel
# 53 = built-in DNS
# 19132/udp = console server list (RakNet); 19132/tcp = NetherNet join
# 19134-19199 = game servers the panel runs (one pair of ports each)
# 49152-50999/udp = Bedrock NetherNet gameplay for those servers
# 19300-19399/udp = the list's own NetherNet gameplay, only used when NetherNet is on
EXPOSE 8443/tcp 53/udp 53/tcp 19132/udp 19132/tcp 19134-19199/udp 19134-19199/tcp 49152-50999/udp 19300-19399/udp
# Game servers get time to save their worlds when the container stops; give
# the container at least STOP_TIMEOUT (30s) plus a little with --stop-timeout.
STOPSIGNAL SIGTERM
ENTRYPOINT ["/usr/local/bin/blockheads"]
