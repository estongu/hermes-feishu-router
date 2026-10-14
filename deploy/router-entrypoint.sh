#!/bin/sh
set -eu

# 在启动前校验必要变量，避免 Go 服务因空配置启动后才产生模糊错误。
: "${FEISHU_APP_ID:?请设置 FEISHU_APP_ID}"
: "${FEISHU_APP_SECRET:?请设置 FEISHU_APP_SECRET}"
: "${PLATFORM_BASE_URL:?请设置 PLATFORM_BASE_URL}"
: "${PLATFORM_USERNAME:?请设置 PLATFORM_USERNAME}"
: "${PLATFORM_PASSWORD:?请设置 PLATFORM_PASSWORD}"

export STORAGE_DRIVER="${STORAGE_DRIVER:-sqlite}"
export SQLITE_PATH="${SQLITE_PATH:-/data/hermes-router.db}"
export MYSQL_HOST="${MYSQL_HOST:-mysql}"
export MYSQL_PORT="${MYSQL_PORT:-3306}"
export MYSQL_DATABASE="${MYSQL_DATABASE:-hermes_router}"
export MYSQL_USER="${MYSQL_USER:-hermes_router}"
export MYSQL_PASSWORD="${MYSQL_PASSWORD:-}"
if [ "$STORAGE_DRIVER" = "mysql" ]; then : "${MYSQL_PASSWORD:?使用 MySQL 时请设置 MYSQL_PASSWORD}"; fi
export FEISHU_APP_NAME="${FEISHU_APP_NAME:-主机器人}"
export FEISHU_STREAM_ENABLED="${FEISHU_STREAM_ENABLED:-true}"
export FEISHU_DEBUG_LOG_EVENTS="${FEISHU_DEBUG_LOG_EVENTS:-false}"

envsubst < /app/router-config.yaml.tmpl > /app/config.yaml
exec /app/hermes-feishu-router
