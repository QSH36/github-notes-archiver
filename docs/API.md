# HTTP API 参考

适用于 `1.0.0`。API 仅通过 SSH 隧道访问。除 `GET /healthz` 与 `POST /api/v1/session` 外均要求管理会话；所有写请求还要求同源 `Origin` 与登录返回的 `X-CSRF-Token`。

| 方法 | 路径 | 用途 |
|---|---|---|
| `GET` | `/healthz` | 服务健康与版本 |
| `POST/DELETE` | `/api/v1/session` | 登录/退出 |
| `GET` | `/api/v1/status` | 汇总状态 |
| `GET/PUT` | `/api/v1/config` | 作者、时区、调度配置 |
| `POST` | `/api/v1/github/discovery-sessions` | 使用临时 PAT 枚举一个 owner 的仓库 |
| `GET` | `/api/v1/github/discovery-sessions/{id}/repositories` | 获取发现结果 |
| `POST` | `/api/v1/repositories/activations` | 批量逐仓安装 Deploy Key |
| `GET` | `/api/v1/repositories` | 已启用/已记录仓库 |
| `PUT` | `/api/v1/repositories/{id}` | 启停或更改归档目录 |
| `POST` | `/api/v1/repositories/{id}/test` | 连接检查 |
| `POST` | `/api/v1/repositories/manual` | 无口令 SSH 私钥手工接入 |
| `POST` | `/api/v1/notes/versions` | 单仓库真实笔记版本入队 |
| `GET` | `/api/v1/queue` | 查看脱敏队列 |
| `POST` | `/api/v1/sync` | 立即同步全部或指定仓库 |
| `POST` | `/api/v1/imports/previews` | 上传 CSV/ZIP 并预览 |
| `POST` | `/api/v1/imports/runs` | 用预览 ID、包哈希和确认标志执行 |
| `GET` | `/api/v1/events` | 最近脱敏事件 |

错误统一为：

```json
{"error":{"code":"invalid_import","message":"说明"}}
```

PAT、管理令牌、SSH 私钥和完整笔记正文不会出现在配置、事件或错误响应中。
