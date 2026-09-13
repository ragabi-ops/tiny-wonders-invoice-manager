# Build
FROM golang:1.26-alpine AS build
WORKDIR /src

# Dependencies first, so a source-only change reuses the module cache.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# Static binary: the runtime image has no libc.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./apps/api

# Run
FROM alpine:3.20
# postgresql16-client must match the postgres:16 server in docker-compose.yml.
# A newer pg_dump writes archives an older server cannot restore cleanly, which
# turns a "successful" backup into an unusable one.
RUN apk add --no-cache ca-certificates tzdata postgresql16-client \
    && adduser -D -u 10001 invoice \
    && mkdir -p /var/lib/invoice/storage \
    && chown -R invoice:invoice /var/lib/invoice

COPY --from=build /out/api /app/api

USER invoice
WORKDIR /app
EXPOSE 8080
ENTRYPOINT ["/app/api"]
