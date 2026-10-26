# Toolbox Windows 桌面版

适用 Windows 11 x64 或更高桌面版本（内部版本号至少 22000）。解压 ZIP 到固定目录，双击 `Toolbox.exe`。程序会检查实际 Windows 版本，不支持 Windows 10 或 Windows Server。

需要 Microsoft Edge WebView2 Evergreen Runtime。Windows 11 通常自带；若系统镜像移除了该运行时，请根据启动提示安装微软官方运行时：https://developer.microsoft.com/microsoft-edge/webview2/ 。

个人版免登录，本机管理员直接使用。首次启动自动在 `%APPDATA%\Toolbox Desktop\` 创建本地配置、密钥和数据库。运行日志在该目录的 `data\runtime-logs\`，页面“系统日志”可查看最近 7 天的日志。请备份整个数据目录，不要只保留数据库而删除 `local.env` 密钥文件。

MCP 的 Node.js / Python 等运行时需要自行安装。标准 Node.js 的 `npx`、`npm` 启动器会直接转换为 Node 脚本入口，不通过命令行拼接执行；其他批处理启动器请改为实际 `.exe` 或 `node` / `python` 加脚本路径。参数逐项填写，含空格的路径无需额外引号。

在“系统设置 → 开机启动”可启用登录 Windows 自动启动，使用当前用户 Run 注册表键，无需管理员权限。请保留程序的固定路径；移动程序后重新开启该选项。关闭窗口会正常退出程序，Windows 版本暂不提供托盘常驻。

升级：先退出旧版本，保留数据目录，将新版本解压到原程序目录再启动。不要同时运行两个版本访问同一个数据目录。此包当前未做 Authenticode 代码签名，Windows 可能显示未知发布者；企业正式分发应使用受信任证书签名。

当前构建在 macOS 交叉编译并校验 PE x64 文件格式，不能替代 Windows 11 真机安装、WebView2、stdio、开机启动与升级回归验收。

## 构建依据

- Wails v2 Windows / WebView2 官方指南：https://wails.io/docs/guides/windows/
- 微软 WebView2 分发说明：https://learn.microsoft.com/microsoft-edge/webview2/concepts/distribution
- Windows 11 版本与内部版本号：https://learn.microsoft.com/windows/release-health/windows11-release-information
- 微软当前用户 Run 注册表键说明：https://learn.microsoft.com/windows/win32/setupapi/run-and-runonce-registry-keys
