FROM golang:1.25-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

FROM alpine:3.21

RUN apk add --no-cache ca-certificates wget && adduser -D -u 10001 filecosystem

COPY --from=builder /out/api /usr/local/bin/api

USER filecosystem
EXPOSE 8080

HEALTHCHECK --interval=15s --timeout=3s --start-period=20s --retries=5 \
    CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1

ENTRYPOINT ["api"]
