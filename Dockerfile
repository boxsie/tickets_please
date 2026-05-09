# syntax=docker/dockerfile:1

FROM golang:1.25-alpine AS builder

RUN apk add --no-cache git ca-certificates

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w" \
    -o /out/tickets_please \
    ./cmd/tickets_please

FROM alpine:3.20

RUN apk add --no-cache ca-certificates git tzdata

COPY --from=builder /out/tickets_please /usr/local/bin/tickets_please

WORKDIR /data

EXPOSE 8765

ENTRYPOINT ["/usr/local/bin/tickets_please"]
CMD ["serve", "--addr", ":8765"]
