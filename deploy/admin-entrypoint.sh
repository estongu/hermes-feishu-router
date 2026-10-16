#!/bin/sh
set -eu

# 后台与 Router 共享数据库配置，以保证管理结果能即时被 Router 读取。
: "${ADMIN_USERNAME:?请设置 ADMIN_USERNAME}"
: "${ADMIN_PASSWORD_HASH:?请设置 ADMIN_PASSWORD_HASH}"
: "${ADMIN_SESSION_SECRET:?请设置 ADMIN_SESSION_SECRET}"

export STORAGE_DRIVER="${STORAGE_DRIVER:-sqlite}"
export SQLITE_PATH="${SQLITE_PATH:-/data/hermes-router.db}"
export MYSQL_HOST="${MYSQL_HOST:-mysql}"
export MYSQL_PORT="${MYSQL_PORT:-3306}"
export MYSQL_DATABASE="${MYSQL_DATABASE:-hermes_router}"
export MYSQL_USER="${MYSQL_USER:-hermes_router}"
export MYSQL_PASSWORD="${MYSQL_PASSWORD:-}"
if [ "$STORAGE_DRIVER" = "mysql" ]; then : "${MYSQL_PASSWORD:?使用 MySQL 时请设置 MYSQL_PASSWORD}"; fi

envsubst < /app/admin-storage.yaml.tmpl > /app/storage-config.yaml
envsubst < /app/admin-config.yaml.tmpl > /app/config.yaml
exec /app/hermes-router-admin /app/config.yaml
