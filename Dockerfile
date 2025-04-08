FROM golang:1.20-alpine AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN go build -o voice-manager


FROM alpine:latest

RUN adduser -D -u 1000 appuser

WORKDIR /home/appuser


COPY --from=builder /app/voice-manager .

RUN chmod +x voice-manager

USER appuser

CMD ["./voice-manager"]
