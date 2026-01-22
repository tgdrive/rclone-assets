# syntax=docker/dockerfile:1
FROM golang:alpine AS builder

RUN apk add --no-cache git

ARG TARGETPLATFORM

ENV CGO_ENABLED=0

WORKDIR /app

COPY go.mod go.sum* ./
RUN go mod download

COPY . .

RUN go build -ldflags="-s -w" -o assets-api .

FROM alpine

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

COPY --from=builder /app/assets-api .

CMD ["/app/assets-api"]
