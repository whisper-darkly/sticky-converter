FROM golang:1.24-alpine AS builder

ARG OVERSEER_VERSION=v2.4.1

RUN apk add --no-cache make git

# Clone overseer so the replace directive in go.mod resolves:
#   replace github.com/whisper-darkly/sticky-overseer/v2 => ../sticky-overseer
#   /src/sticky-refinery/../sticky-overseer = /src/sticky-overseer ✓
RUN git clone --depth=1 --branch=${OVERSEER_VERSION} \
    https://github.com/whisper-darkly/sticky-overseer.git \
    /src/sticky-overseer

WORKDIR /src/sticky-refinery
COPY . ./
RUN make install PREFIX=/out


FROM lscr.io/linuxserver/ffmpeg:latest

COPY --from=builder /out/bin/sticky-refinery /usr/local/bin/sticky-refinery

ENV OVERSEER_LISTEN=:8080
ENV OVERSEER_DB=/data/refinery.db

EXPOSE 8080

HEALTHCHECK --interval=15s --timeout=5s --start-period=10s --retries=3 \
    CMD curl -fsS http://localhost:8080/openapi.json > /dev/null || exit 1

# Override linuxserver's default ffmpeg entrypoint
ENTRYPOINT ["/usr/local/bin/sticky-refinery"]
CMD ["-config", "/data/config.yaml"]
