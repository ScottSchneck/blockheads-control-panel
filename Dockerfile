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
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata libcap && \
    mkdir -p /config /data && chown 99:100 /config /data
COPY --from=build /out/blockheads /usr/local/bin/blockheads
# Lets the panel use port 53 (DNS) and 80/443 without running as root.
RUN setcap cap_net_bind_service=+ep /usr/local/bin/blockheads && apk del libcap
# 99:100 is nobody:users, the owner Unraid uses for appdata.
USER 99:100
VOLUME ["/config", "/data"]
# 53 = built-in DNS
# 19132/udp = console server list (RakNet); 19132/tcp = NetherNet join
# 19300-19399/udp = NetherNet gameplay (WebRTC), only used when NetherNet is on
EXPOSE 53/udp 53/tcp 19132/udp 19132/tcp 19300-19399/udp
ENTRYPOINT ["/usr/local/bin/blockheads"]
