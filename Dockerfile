FROM golang:1.24-alpine AS builder
WORKDIR /app
COPY go.mod .
COPY *.go .
RUN go build -o runner .

FROM alpine:3.21
RUN apk add --no-cache python3 nodejs php83 && \
    ln -sf /usr/bin/php83 /usr/bin/php
COPY --from=builder /app/runner /usr/local/bin/runner
EXPOSE 3000
CMD ["runner"]
