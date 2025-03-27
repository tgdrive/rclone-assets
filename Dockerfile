# syntax=docker/dockerfile:1

FROM golang:alpine AS builder

RUN apk add --no-cache git gcc musl-dev

ARG TARGETPLATFORM
ARG BUILDPLATFORM
ARG TARGETOS
ARG TARGETARCH

WORKDIR /app

COPY go.mod go.sum* ./
RUN go mod download

COPY . .

RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} CGO_ENABLED=0 \
    go build -ldflags="-s -w" -o rclone-api .
    
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

COPY docker-entrypoint.sh /
RUN chmod +x /docker-entrypoint.sh

ENTRYPOINT ["/docker-entrypoint.sh"]