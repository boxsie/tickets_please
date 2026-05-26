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

# Run as a non-root user (serves security-hardening #1075af87). /data is a
# hostPath volume at runtime, so the bind-mounted dir's ownership is handled
# k8s-side via fsGroup:10001 (a chown-Job seeds an existing dir); the in-image
# chown covers the fresh-image case.
RUN adduser -u 10001 -D app && chown -R 10001:10001 /data
USER 10001

EXPOSE 8765

ENTRYPOINT ["/usr/local/bin/tickets_please"]
CMD ["serve", "--addr", ":8765"]
