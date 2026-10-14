# syntax=docker/dockerfile:1

# Router 与后台使用独立 Go module，因此分别构建，避免运行镜像携带编译工具链。
FROM golang:1.26-alpine AS router-builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/hermes-feishu-router ./cmd/server

FROM golang:1.26-alpine AS admin-builder
WORKDIR /src/admin
COPY admin/go.mod admin/go.sum ./
RUN go mod download
COPY admin/ .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/hermes-router-admin .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/password-hash ./cmd/password-hash

FROM alpine:3.21 AS router
RUN apk add --no-cache ca-certificates gettext \
    && addgroup -S hermes \
    && adduser -S -G hermes hermes
WORKDIR /app
COPY --from=router-builder /out/hermes-feishu-router /app/hermes-feishu-router
COPY deploy/router-config.yaml.tmpl deploy/router-entrypoint.sh /app/
RUN chmod +x /app/router-entrypoint.sh && chown -R hermes:hermes /app
USER hermes
ENTRYPOINT ["/app/router-entrypoint.sh"]

FROM alpine:3.21 AS admin
RUN apk add --no-cache ca-certificates gettext \
    && addgroup -S hermes \
    && adduser -S -G hermes hermes
WORKDIR /app
COPY --from=admin-builder /out/hermes-router-admin /app/hermes-router-admin
COPY --from=admin-builder /out/password-hash /app/password-hash
COPY deploy/router-config.yaml.tmpl deploy/admin-config.yaml.tmpl deploy/admin-entrypoint.sh /app/
RUN chmod +x /app/admin-entrypoint.sh && chown -R hermes:hermes /app
USER hermes
ENTRYPOINT ["/app/admin-entrypoint.sh"]
