FROM golang:1.24-alpine AS builder

RUN apk add --no-cache make

WORKDIR /src/sticky-refinery
COPY . ./
RUN make install PREFIX=/out

FROM alpine:3.21

RUN apk add --no-cache ffmpeg

COPY --from=builder /out/bin/sticky-refinery /usr/local/bin/sticky-refinery

ENV REFINERY_LISTEN=:8080
ENV REFINERY_DB=/data/refinery.db
ENV REFINERY_TRUSTED_CIDRS=

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD wget -qO- http://localhost:8080/health || exit 1

CMD ["sticky-refinery", "-config", "/data/config.yaml"]
