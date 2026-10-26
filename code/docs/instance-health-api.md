# MCP 实例分层状态

GET /api/v1/instances 和单实例接口新增 health，不改变原有 status 与 Gateway fail-closed 授权条件。

health 包含 process、protocol、business，每层有 state/detail/checked_at，另有 tool_count、last_tool/last_tool_at。

- process：running/stopped/unknown/not_applicable。stdio 根据进程存在性观测；HTTP 不管理远端进程。进程存活不等于协议成功。
- protocol：passed/failed/unverified/disconnected。通过 MCP 初始化及工具发现才为 passed；连接结束为 disconnected。
- business：passed/failed/unverified。普通服务没有业务探针时一直未验证；参考 PostgreSQL 执行只读 SELECT current_database()，仅证明数据库连接查询通过，不代表全部业务。
- PG 探针失败后关闭候选连接，页面明确显示协议曾通过、连接已断开、业务失败，不能声称当前进程仍运行或仍已连接。
- 最近业务调用成功只记录对应工具名和时间，不自动将所有业务标成通过。连接关闭/进程退出时取消当前业务验证证明。
- 状态证据属于当前管理连接及配置版本；失败替换不覆盖旧有效连接的证明；重启重新探测，不复用旧证明。手动停止、隔离与不确定调用不自动放开。

0.1.8 页面仅展示进程状态、MCP 测试两列及“MCP 已连接”统计，移除工具测试列与业务探针相关筛选、统计。health.business 保留为内部连接验证证据，避免改变已有健康检查与鉴权行为。页面每5秒读取状态；后台连接健康检查默认30秒，页面刷新不执行工具业务调用。
