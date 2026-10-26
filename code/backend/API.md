# 管理 API（V3 当前实现）

前缀 `/api/v1`，同源 Cookie 会话。JSON 失败 `{ "error": "说明" }`，集合 `{ "items": [], "total": 0 }`。修改操作需要管理员，logout 例外。POST/PATCH JSON 使用 `Content-Type: application/json`。原生服务同时提供内嵌 Web 页面；Vite proxy 仅供开发。

- `GET /setup` -> `{required}`；`POST /setup {username,password,setup_key}`，密码 12–72 字节。
- `POST /login {username,password}` -> 用户对象（含 `must_change_password`）；`GET /me`；`POST /logout`；`POST /account/password {current_password,new_password}` 修改本人密码并使原会话失效。初始管理员改密前其余管理接口返回 403。
- `GET /users`、`POST /users {username,password,role}`；`PATCH /users/:id {role?,enabled?,password?}`。
- `GET /instances`、`GET /instances/:id`；`POST /instances`、`PATCH /instances/:id`。
  实例完整字段：`id,name,description,account_label,transport(http|stdio),url,command,args:[],cwd,concurrency(1–20),timeout_seconds(1–600),version,enabled,status,last_error`。
  新建后自动排队启动并执行 MCP 初始化与工具发现；响应包含 `start_task_id`，连接失败时实例显示 fault，可重新 start。PATCH 提交完整非秘密配置及原 version。运行配置需 restart 才应用，保存不会自动重启。
  `env`/`headers` 是秘密 map，未传或 null 保留、传 `{}` 清除、非空整体替换。返回只有 `env_keys`/`header_keys`，不返回秘密值。
- `DELETE /instances/:id`；`POST /instances/:id/start|stop|restart` -> 202 `{id,kind,status}` 任务。
- `GET /instances/:id/postgres`（管理员）返回单 URI PostgreSQL stdio 的结构化脱敏连接配置；`POST /instances/:id/postgres {version,connections:[{name,host,port,database,username,sslmode,password_mode:keep|replace|none,password}]}` 单事务保存当前连接及至多19个新增连接。每个数据库复用程序、独立实例；响应 `start_task_id`，候选实际执行只读 SQL 成功才发布，配置失败保留旧运行版本。
- `GET /instances/:id/tools` -> `{items: MCP Tool[],total,effective_version}`。工具列表名称是下游名称，网关使用稳定实例 ID+工具哈希名称。
- `GET /rules`、`POST /rules {cidr,effect:allow|deny}`、`DELETE /rules/:id`；全局来源 IP 规则，支持单个 IPv4/IPv6 地址或 CIDR。无允许规则时有效 Token 可连接；有允许规则时只放行匹配地址，禁止规则优先。仅使用 TCP 连接的实际来源地址，不采信 X-Forwarded-For。
- `GET /tokens`、`POST /tokens {user_id,client_name,expires_at}` -> `{id,token,user_id,client_name,expires_at}`，明文只返回一次；`DELETE /tokens/:id` 撤销。expires_at 为 RFC3339。
- `GET /logs` 支持 limit(默认50，最大200)、offset，以及 status/user_id/client_name/instance_id/tool_name/from/to 精确过滤；时间为RFC3339。total是匹配总数；普通用户始终仅本人。
- `GET /clients` 当前为 Token 客户端接入记录，不代表宿主软件自动识别。
- `GET /gateway` -> `{url,template}`，模板含 `<TOKEN>` 占位。
- `GET /tasks`、`GET /tasks/:id`（管理员） -> 任务字段 `id,kind,status(queued|running|succeeded|failed),detail,result,created_at`。
- `GET /scan/sources` 查询可扫描来源；`POST /scan {paths:[]}` -> 任务，读取允许范围内的实际客户端配置；`GET /discoveries` 返回脱敏发现记录和导入预览；`POST /discoveries/:id/import {fingerprint}` 显式确认导入。确认纳管后自动排队启动并发现工具，响应包含 `start_task_id`；Gateway 调用还需有效 Token，并受来源 IP 规则限制。
- `POST /discoveries/batch-import {items:[{id,fingerprint}]}` 批量纳管 1–100 项可纳管配置，按来源重新校验指纹并在同一事务中保存；保存阶段任一项失败则全部回滚。提交后自动排队启动，任务结果逐项记录 ready/fault。页面默认选中全部未纳管且可纳管的项。
- `GET /system/scan-roots` 查看页面与部署配置的扫描目录；`POST /system/scan-roots {path}` 添加自动扫描目录；`DELETE /system/scan-roots {path}` 移除页面添加的目录。仅管理员可操作，页面目录保存在 SQLite 中并随备份恢复。
- `POST /system/native-stdio {enabled:boolean}` 在本机模式切换 stdio 命令运行许可；关闭前必须停止所有 stdio 实例。设置保存在 SQLite；部署强制启用时页面只读。
- HTTP 实例支持 `header_helper` 动态请求头命令（输出 JSON 对象），用于短期口令；命令加密保存、不回显、无 shell 解析，每次 HTTP 请求前在本机以 Toolbox 服务账户运行。须先启用本机命令运行许可。扫描 Codex `http_headers_helper` 时保留为可纳管配置。
- `POST /install/offline`（管理员，multipart：`file` ZIP、`config` JSON `{name,runtime:node|python3|binary,entry,args:[],env:{}}`）→ 异步任务；ZIP 上传128 MB、解压512 MB、10000项限制。拒绝路径穿越、符号链接、特殊文件及重复条目；离线解包后自动创建实例并启动。任务 `result.instance_id` 关联实例，`stage/message` 记录实际结果。安装文件位于 SQLite 同目录 artifacts 或 ARTIFACT_DIR，不在 SQLite 备份范围。
- `GET /system/catalog-registry`、`POST /system/catalog-registry {registries:[{name,url}]}`（管理员）配置最多8个兼容官方 v0.1 API 的 HTTPS 源，兼容旧 `{registry}` 字段。保存返回各源实际检测结果，不可用来源可保留且明确告警。
- `GET /catalog?q=&cursors=` 并发查询各源（每源30秒），名称搜索、latest版本过滤、按name+version去重。返回 `items,sources,next_cursor`；next_cursor作为下页cursors原样传回。来源失败不隐藏其他来源结果。
- `POST /install {ecosystem:npm|pypi,package,version}` -> 任务。仅准备制品不自动创建实例；需要明确开发模式或受限运行环境。
- `GET /system/status`（管理员）查询 SQLite、WAL、运行实例和本机环境；`POST /system/backup` 下载在线一致性快照。
- `POST /system/restore`（multipart `file`）校验并暂存 SQLite 备份，返回 `restore_id` 与计数；`POST /system/restore/:id/commit {confirm:"RESTORE"}` 二次确认恢复。成功后所有 Web 会话失效，需重新登录。
- `/mcp`：官方 SDK Streamable HTTP，Bearer Token，有状态 MCP 会话；有效 Token 可发现并调用已启动实例的工具；全局来源 IP 规则在 Gateway 入口校验。一个上游会话对每个 stdio 实例建立独立下游连接，关闭或超时后清理。

除 logs 外，total 是返回条数；tasks 最多返回最近 100 条，catalog 每源每页最多100条并支持游标分页，其余集合尚未实现分页或数量限制。实现范围与实际验收结果以 `../test-results/v2-acceptance.md` 为准。

Gateway 对下游明确返回的 JSON-RPC 工具错误返回 `CallToolResult.isError=true`，审计标记 failed，实例保持可用；超时/断线导致执行结果未知时仍标记 unknown 并隔离，不自动重放。PostgreSQL 参考服务启动需实际只读 SQL 验证，握手与工具发现不等于数据库可访问。
