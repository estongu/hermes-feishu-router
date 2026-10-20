# syntax=docker/dockerfile:1

# Router 与后台使用独立 Go module，因此分别构建，避免运行镜像携带编译工具链。
# 国内服务器访问 proxy.golang.org 可能超时；可在构建时通过 --build-arg GOPROXY 覆盖。
ARG GOPROXY=https://goproxy.cn,direct

FROM golang:1.26-alpine AS router-builder
ARG GOPROXY
ENV GOPROXY=${GOPROXY}
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/hermes-feishu-router ./cmd/server

FROM golang:1.26-alpine AS admin-builder
ARG GOPROXY
ENV GOPROXY=${GOPROXY}
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
RUN mkdir /data && chmod +x /app/router-entrypoint.sh && chown -R hermes:hermes /app /data
USER hermes
ENTRYPOINT ["/app/router-entrypoint.sh"]

FROM alpine:3.21 AS admin
RUN apk add --no-cache ca-certificates gettext \
    && addgroup -S hermes \
    && adduser -S -G hermes hermes
WORKDIR /app
COPY --from=admin-builder /out/hermes-router-admin /app/hermes-router-admin
COPY --from=admin-builder /out/password-hash /app/password-hash
COPY deploy/admin-storage.yaml.tmpl deploy/admin-config.yaml.tmpl deploy/admin-entrypoint.sh /app/
RUN mkdir /data && chmod +x /app/admin-entrypoint.sh && chown -R hermes:hermes /app /data
USER hermes
ENTRYPOINT ["/app/admin-entrypoint.sh"]
