# 客户端一键接入接口

管理员专用，复用现有会话鉴权和来源校验。配置文件写在 **Toolbox 所在机器**；浏览器无法操作另一台客户端机器。个人版通过 Wails 私有桥调用相同接口。

| 接口 | 行为 |
|---|---|
| GET /api/v1/client-integrations/targets | Codex、Qoder 桌面/CLI、Qoder CN 桌面/CLI 与 IDE 的默认目录和文件位置 |
| POST /api/v1/client-integrations/preview | 输入 client、directory、server_name、user_id、days、never_expires；返回实际路径、文件指纹、冲突信息、脱敏配置片段及 request_id，不签发 Token、不写配置 |
| POST /api/v1/client-integrations | 回传预览内容及 replace；确认未变更后备份、签发独立 Token、原子写入配置。相同 request_id 重试不重复发 Token |
| GET /api/v1/client-integrations | 仅返回接入元数据、Token 撤销状态及最近访问，不返回 Token 或原配置秘密 |
| DELETE /api/v1/client-integrations/:id | 撤销该 Token，移除本次接入项或恢复原同名项，保留其他客户端设置和之后的无关修改 |

客户端类型：codex → config.toml；qoder → ~/.qoder/settings.json；qoder-cn → shared_client/mcp.json；qoder-cn-legacy → mcp.json；qoder-cn-cli → settings.json（桌面 / CLI）。默认 ~/.codex（或 CODEX_HOME）、~/.qoder、~/.qoder-cn；directory 是根目录，桌面支持系统目录选择器。

配置使用 HTTP Gateway 和独立 Bearer Token。有效期1–365天，默认30天；never_expires=true 表示永久有效，忽略 days；用户及角色继续决定授权范围。不会设置客户端 trust/alwaysAllow 或跳过客户端自身的权限确认。写入后需在客户端重载 MCP/重新启动，写入状态与实际访问分别展示，均不代表所有业务已验证。

保护措施：

- 同名配置默认拒绝覆盖；预览确认 replace 才替换。预览时不展示旧配置密钥。
- JSON 保留全部其他字段和大整数；Codex TOML 保留其他设置、注释和 MCP，支持标准表结构。内联/点号 MCP 定义无法安全局部修改时拒绝操作，原文件不变。
- 现有文件写入前完整备份到同目录 *.toolbox-backup-<id>.bak，0600。新配置临时文件0600、fsync后原子替换。拒绝损坏配置、过大文件、非普通文件和预览后的并发变更。
- 接入记录和必要的恢复信息通过 MASTER_KEY 加密后保存到 system_settings，Token 表仅保存 hash；数据库恢复前验证接入记录密钥。
- pending 记录先持久化、Token 初始过期；配置写入完成再更新有效期激活（显式撤销后的 Token 不会被重试重新激活）。崩溃或失败可用原 request_id/“重试接入”恢复。removing 表示 Token 已撤销、配置还原待完成，可继续撤销。
- 撤销遇到同名项被手工修改时，不覆盖其改动，提示到 Gateway 页面单独撤销 Token；其他 MCP 不受影响。
- 401 未登录，403 非管理员，400 参数/格式错误，409 冲突或并发修改，503 文件/存储不可用。POST pending恢复成功200，首次完成201。

格式依据：[Codex 官方 MCP 文档](https://learn.chatgpt.com/docs/extend/mcp?surface=cli)、[Qoder CN 官方 MCP 参考](https://docs.qoder.cn/cli/mcp-reference)。IDE 共享位置同时依据本机 Qoder CN 的实际配置目录识别，不将 CLI settings.json 与 IDE mcp.json 混为一个来源。

Qoder 桌面/CLI 用户级格式依据：[Qoder MCP 参考](https://docs.qoder.com/cli/mcp-reference)。Qoder CN 桌面与 IDE 使用不同文件，接入前按实际客户端选择版本；不能将后台 IDE 访问视为桌面连接器验收通过。

## Token 永久有效（0.1.7）

POST /api/v1/tokens 支持 never_expires=true，此时 expires_at 可省略。普通限时 Token 仍要求未来的 expires_at。客户端一键接入 preview/apply 也支持 never_expires，列表接口 /tokens、/clients 和 /client-integrations 均返回该布尔字段；为 true 时前端显示“永久有效”，不解析 expires_at 作为有效期。

SQLite 迁移11新增 never_expires，已有 Token 默认 false，保留原有效期。永久 Token 的鉴权仍校验撤销、客户端删除、用户启用与角色权限；一键接入 pending 阶段保持过期且标记 false，仅配置写入成功后激活。撤销后重试不能恢复永久权限。
