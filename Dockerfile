# ----------------------------------------------
# Stage 1: Build the Go binary
# ----------------------------------------------
FROM golang:1.20-alpine AS builder

# Create app directory inside container
WORKDIR /app

# Copy go.mod and go.sum first (for caching dependencies)
COPY go.mod go.sum ./
RUN go mod download

# Now copy the rest of your source code
COPY . .

RUN go build -o voice-manager

# ----------------------------------------------
# Stage 2: Final minimal image
# ----------------------------------------------
FROM alpine:latest

# Create a user for security (optional but recommended)
RUN adduser -D -u 1000 appuser

WORKDIR /home/appuser

# Copy the binary from builder stage
COPY --from=builder /app/voice-manager .

# Make the binary executable
RUN chmod +x voice-manager

# Switch to non-root user
USER appuser

# The bot doesn't expose a traditional TCP/HTTP port. 
# But if you did need to, you'd use: EXPOSE <port>

# By default, run the compiled binary
CMD ["./voice-manager"]
