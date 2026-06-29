FROM golang:1.24-alpine AS builder
WORKDIR /app
COPY go.mod .
COPY *.go .
RUN go build -o runner .

FROM alpine:3.21 AS txiki-builder
RUN apk add --no-cache git build-base cmake linux-headers libffi-dev
RUN git clone --depth 1 --branch v26.6.0 --recurse-submodules \
        https://github.com/saghul/txiki.js.git /src && \
    cd /src && make

FROM alpine:3.21
COPY --from=txiki-builder /src/build/tjs /usr/local/bin/tjs
RUN apk add --no-cache python3 py3-requests php83 bubblewrap libffi && \
    ln -sf /usr/bin/php83 /usr/bin/php && \
    addgroup -S runner && adduser -S -G runner runner
COPY --from=builder /app/runner /usr/local/bin/runner
USER runner
EXPOSE 3000
CMD ["runner"]
