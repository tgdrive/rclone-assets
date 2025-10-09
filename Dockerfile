# syntax=docker/dockerfile:1

FROM tonistiigi/xx AS xx

FROM golang:alpine AS builder

COPY --from=xx / /

RUN apk add --no-cache git gcc musl-dev

ARG TARGETPLATFORM

ENV CGO_ENABLED=0

WORKDIR /app

COPY go.mod go.sum* ./
RUN go mod download

COPY . .

RUN xx-go build -ldflags="-s -w" -o rclone-api .
    
FROM alpine

RUN apk add --no-cache ca-certificates tzdata fuse3 curl bash su-exec

RUN echo "user_allow_other" >> /etc/fuse.conf

RUN mkdir -p /app/rclone-assets

WORKDIR /app

COPY --from=builder /app/rclone-api .

COPY --from=ghcr.io/tgdrive/rclone /usr/local/bin/rclone /usr/bin/rclone

RUN chmod +x /app/rclone-api

RUN addgroup -S appgroup && adduser -S appuser -G appgroup
RUN chown -R appuser:appgroup /app /app/rclone-assets

ENV XDG_CONFIG_HOME=/config

COPY docker-entrypoint.sh /
RUN chmod +x /docker-entrypoint.sh

ENTRYPOINT ["/docker-entrypoint.sh"]
