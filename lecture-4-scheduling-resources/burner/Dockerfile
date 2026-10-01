# syntax=docker/dockerfile:1
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /burner .

FROM scratch
COPY --from=build /burner /burner
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/burner"]
