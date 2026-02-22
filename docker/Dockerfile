FROM golang:1.24-alpine AS builder

RUN apk add --no-cache make

WORKDIR /src/sticky-refinery
COPY . ./
RUN make install PREFIX=/out

FROM lscr.io/linuxserver/ffmpeg:latest

COPY --from=builder /out/bin/sticky-refinery /usr/local/bin/sticky-refinery

ENV OVERSEER_LISTEN=:8080
ENV OVERSEER_DB=/data/refinery.db

EXPOSE 8080

# Override linuxserver's default ffmpeg entrypoint
ENTRYPOINT ["/usr/local/bin/sticky-refinery"]
CMD ["-config", "/data/config.yaml"]
