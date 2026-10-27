# This tag names the Go that CI, the gate and the image run on (ADR 0005), so
# moving to a new Go, by hand or by a Dependabot update, changes this line
# alone. Keep it one exact release. The gate fails a floating tag such as
# golang:1.27, and fails a toolchain line in go.mod.
FROM golang:1.27.2 AS builder

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