FROM golang:1.24-alpine AS builder

RUN apk add --no-cache make

WORKDIR /src/sticky-refinery
COPY . ./
RUN make install PREFIX=/out

FROM alpine:3.21

RUN apk add --no-cache ffmpeg

COPY --from=builder /out/bin/sticky-refinery /usr/local/bin/sticky-refinery

ENV OVERSEER_LISTEN=:8080
ENV OVERSEER_DB=/data/refinery.db

EXPOSE 8080

CMD ["sticky-refinery", "-config", "/data/config.yaml"]
