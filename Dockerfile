# The Go that builds the image, which must be the toolchain go.mod pins: CI's
# tests run on that one. The gate's image Go step fails when they differ, so a
# Dependabot bump of this tag needs go.mod's toolchain line moved with it.
FROM golang:1.27.1 AS builder

ARG VERSION=dev

WORKDIR /app

COPY go.mod go.sum ./

RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w -X main.Version=${VERSION}" -o main .

FROM alpine:latest

# ffmpeg builds each recording's mix (spec #381). Running it here fails the
# build of an image that couldn't.
RUN apk add --no-cache ffmpeg && ffmpeg -version

WORKDIR /app

COPY --from=builder /app/main .

CMD ["./main"]