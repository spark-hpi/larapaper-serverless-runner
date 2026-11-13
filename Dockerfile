FROM golang:1.24-alpine AS builder
WORKDIR /app
COPY . /app/
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o runner .

FROM alpine:3.21 AS txiki-builder
RUN apk add --no-cache git build-base cmake linux-headers libffi-dev
RUN git clone --depth 1 --branch v26.6.0 --recurse-submodules \
        https://github.com/saghul/txiki.js.git /src && \
    cd /src && make

FROM alpine:3.21 AS base
RUN apk add --no-cache \
        bubblewrap ca-certificates \
        python3 py3-requests \
        php84 php84-curl php84-openssl php84-mbstring \
        ruby ruby-json ruby-bigdecimal && \
    gem install --no-document httparty && \
    ln -sf /usr/bin/php84 /usr/bin/php && \
    addgroup -S runner && adduser -S -G runner runner
COPY --from=builder /app/runner /usr/local/bin/runner
EXPOSE 3000
HEALTHCHECK --interval=30s --timeout=3s CMD wget -qO- http://127.0.0.1:3000/health >/dev/null || exit 1

# Slim variant: JS runs on txiki.js. No Intl, require, process, Buffer or node:* modules.
FROM base AS slim
RUN apk add --no-cache libffi
COPY --from=txiki-builder /src/build/tjs /usr/local/bin/tjs
ENV TRANSFORM_NODE_RUNTIME=txiki
USER runner
CMD ["runner"]

# Default variant: JS runs on Node.js with full ICU data for Intl.
FROM base AS default
RUN apk add --no-cache nodejs icu-data-full
ENV TRANSFORM_NODE_RUNTIME=node
USER runner
CMD ["runner"]
