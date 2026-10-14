#!/bin/sh
set -eu

# 后台与 Router 共享同一份数据库配置，以保证管理结果能即时被 Router 读取。
: "${MYSQL_PASSWORD:?请设置 MYSQL_PASSWORD}"
: "${ADMIN_USERNAME:?请设置 ADMIN_USERNAME}"
: "${ADMIN_PASSWORD_HASH:?请设置 ADMIN_PASSWORD_HASH}"
: "${ADMIN_SESSION_SECRET:?请设置 ADMIN_SESSION_SECRET}"

export MYSQL_HOST="${MYSQL_HOST:-mysql}"
export MYSQL_PORT="${MYSQL_PORT:-3306}"
export MYSQL_DATABASE="${MYSQL_DATABASE:-hermes_router}"
export MYSQL_USER="${MYSQL_USER:-hermes_router}"

envsubst < /app/router-config.yaml.tmpl > /app/router-config.yaml
envsubst < /app/admin-config.yaml.tmpl > /app/config.yaml
exec /app/hermes-router-admin /app/config.yaml
