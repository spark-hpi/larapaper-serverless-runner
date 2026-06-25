FROM golang:1.24-alpine AS builder
WORKDIR /app
COPY go.mod .
COPY *.go .
RUN go build -o runner .

FROM alpine:3.21
RUN apk add --no-cache python3 nodejs php83 libcap && \
    ln -sf /usr/bin/php83 /usr/bin/php && \
    addgroup -S runner && adduser -S -G runner runner
COPY --from=builder /app/runner /usr/local/bin/runner
RUN setcap 'cap_setuid+ep cap_setgid+ep' /usr/local/bin/runner
USER runner
EXPOSE 3000
CMD ["runner"]
