FROM golang:1.21-alpine AS builder

ARG VERSION=dev

WORKDIR /build

# Copy go mod files
COPY go.mod go.sum ./
RUN go mod download

# Copy source code (only main.go, not proxy.go)
COPY main.go ./

# Build the binary with version injected
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags "-s -w -X main.version=${VERSION}" -o talos-remote-config main.go

# Final stage
FROM alpine:latest

RUN apk --no-cache add ca-certificates wget

WORKDIR /app

COPY --from=builder /build/talos-remote-config .

EXPOSE 8080

ENTRYPOINT ["./talos-remote-config"]
