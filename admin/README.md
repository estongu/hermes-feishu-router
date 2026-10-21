# Hermes Router Admin

这是独立运行的后台管理服务，与消息 Router 分属不同进程，仅共享同一数据库（默认 SQLite，也支持 MySQL）。

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

后台的“批量导入”支持用一份 CSV 同时写入内部、外部路由。格式为：

```csv
type,feishu_user_id,app_id,open_id,container_id,username,enabled
internal,user_123,,,hermes-alice,alice,true
external,,cli_xxx,ou_xxx,hermes-guest,guest,true
```

`type` 为 `internal` 时只填写 `feishu_user_id`，为 `external` 时只填写 `app_id` 与 `open_id`。导入会更新同一身份已有路由，且会自动创建 CSV 中不存在的容器（`profile=default`、启用）。文件会先整体校验，再在单个事务中写入，任一行出错不会产生部分结果。

待配置成员绑定后，后台会在一个事务中写入外部路由并将该待配置记录标记为 `bound`。Router 的下一条消息会立即使用新路由，无需重启。
