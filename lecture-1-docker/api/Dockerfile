# syntax=docker/dockerfile:1

# Build stage
FROM golang:1.19.8 AS builder
WORKDIR /app

COPY go.mod ./
COPY . .

RUN CGO_ENABLED=0 go build -o api .

# Minimal runtime stage (build with --target minimal)
FROM scratch AS minimal
WORKDIR /app
COPY --from=builder /app/api /app/api
USER 65532:65532
ENTRYPOINT ["/app/api"]

# Default runtime stage
FROM alpine:3.24.1
RUN addgroup -S api && adduser -S -G api api
WORKDIR /app

COPY --from=builder --chown=api:api /app/api /app/api
USER api
ENTRYPOINT ["/app/api"]
