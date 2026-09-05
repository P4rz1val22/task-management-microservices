# Build stage
FROM golang:1.24.5-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o main .

FROM alpine:latest

RUN apk --no-cache add ca-certificates

WORKDIR /root/

COPY --from=builder /app/main .

# No EXPOSE: this service serves no HTTP. It is a consumer only, which is why
# it also has no healthcheck in docker-compose.yml -- there is nothing to probe.

CMD ["./main"]
