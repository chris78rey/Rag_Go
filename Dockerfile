# Stage 1: Build
FROM golang:1.22-alpine AS builder

WORKDIR /app

COPY go.mod ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /server ./cmd/server

# Stage 2: Runtime
FROM alpine:3.20

RUN apk add --no-cache poppler-utils ca-certificates tzdata

WORKDIR /app

COPY --from=builder /server /usr/local/bin/server
COPY --from=builder /app/web ./web

RUN mkdir -p /app/uploads /tmp

EXPOSE 8080

ENTRYPOINT ["server"]
