# syntax=docker/dockerfile:1

# Build stage
FROM golang:1.19.8 AS builder
WORKDIR /app

COPY go.mod ./
COPY . .

RUN CGO_ENABLED=0 go build -o api .

# Runtime stage
FROM alpine:3.24.1
RUN addgroup -S api && adduser -S -G api api
WORKDIR /app

COPY --from=builder --chown=api:api /app/api /app/api
USER api
ENTRYPOINT ["/app/api"]
