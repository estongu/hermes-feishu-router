# Hermes Router Admin

这是独立运行的后台管理服务，与消息 Router 分属不同进程，仅共享 MySQL。

## 启动

```bash
cd admin
cp config.yaml.example config.yaml
go run ./cmd/password-hash -password '请使用强密码'
# 将输出填入 config.yaml 的 auth.password-hash，并设置至少 32 字节 auth.session-secret
go run .
```

默认监听 `http://127.0.0.1:8081`。请仅通过内网访问，或在反向代理层启用 HTTPS。

## 管理内容

- `ekko_container`：容器 ID、profile 与启用状态；
- `feishu_user_route`：内部用户 `user_id` 路由；
- `feishu_external_route`：外部成员的 `app_id + open_id` 路由；
- `feishu_unmatched_identity`：Router 发现但尚未配置的外部成员，含最多 200 字的最新消息摘要。

待配置成员绑定后，后台会在一个事务中写入外部路由并将该待配置记录标记为 `bound`。Router 的下一条消息会立即使用新路由，无需重启。
