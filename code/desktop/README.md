# Toolbox 本地桌面版

使用 Go + Wails，直接加载 Web 版的同一套 Vue 页面与业务逻辑。本地数据库、密钥与企业 Web 服务器独立，不要求连接企业服务器。0.1.1 起为个人免登录模式，启动后直接以本机超级管理员身份使用，没有登录、改密、用户管理与角色分配入口。

## 构建

在 `code` 目录执行 `./scripts/build-desktop.sh native`。需要 Go 1.25.2、Node.js，以及对应系统的桌面 SDK；macOS 需要 Xcode Command Line Tools。Linux 需安装 GTK3、WebKitGTK 4.1 的开发依赖。Windows 11 x64 桌面版支持在 macOS/Linux 交叉构建，使用纯 Go 的 Windows WebView2 后端；运行需要 Microsoft Edge WebView2 Evergreen Runtime。详见 [Windows 说明](README-windows.md)。

macOS 的 `.app` 在 `desktop/build/bin/Toolbox.app`，网页下载的 ZIP 在 `dist/desktop/`。脚本计算实际包大小、SHA256，并更新同目录的 `manifest.json`。企业 Web 版可在系统设置的软件包管理中上传、发布、下架和归档版本，默认保存到程序旁的 `packages/`，可通过 `toolbox.json` 或页面配置目录。兼容可执行程序旁 `desktop/` 的旧 manifest，也可用 `TOOLBOX_DESKTOP_RELEASES_DIR` 指定目录。上传不会执行制品。

桌面版与纯 Go 服务端二进制分开：服务端可在 ARM Mac 上交叉编译 Linux AMD64；macOS/Linux 桌面版需要在有目标系统 SDK 的主机上构建；Windows x64 桌面版可交叉构建。只有已生成的包才展示在 `/download` 页面。

## 本机运行

macOS 解压后打开 `Toolbox.app`，直接进入 MCP 管理。首次运行自动创建 `~/Library/Application Support/Toolbox Desktop/`，其中 `local.env` 保存自动生成的密钥，`data/toolbox.sqlite` 保存数据，`data/artifacts/` 与 `data/audit/` 分别保存本地安装制品和审计日志，`toolbox.log` 记录本机服务启动日志。不要删除或替换已有数据对应的密钥。

个人版不需要初始账号或密码。旧版本地数据库可直接继续使用，已有配置、凭据和 Token 不会被清空，已有账号密码不会被改写。用于审计和 Token 归属的内部身份仍保留在数据库中，不作为用户操作入口。旧 Token 若绑定其他受限用户，可撤销后重新签发本机 Token。

个人免登录能力只提供给 Wails 进程内的 API 桥接，不通过 HTTP 请求头或公开 Cookie 传递；直接访问回环 TCP 管理 API 仍会被拒绝。客户端使用 `/mcp` 时仍需专属 Token。新 Token 自动归属本机管理员，无需选用户，默认可使用全部 MCP 和工具。企业 Web 版继续使用登录与角色权限。

默认 Gateway 端口为 8090；被其他程序占用时自动选择空闲端口并保存。可在“系统设置”修改端口；修改后桌面窗口继续使用，外部 MCP 客户端需更新 Gateway 地址。

macOS 关闭窗口会隐藏到后台，菜单栏的 Toolbox 图标或应用菜单可重新显示窗口；选择“退出 Toolbox”才停止本机服务与托管 MCP。重复打开同一数据目录的应用会激活已有窗口，数据库文件锁同时防止重复占用。

企业 Web 版与个人版的 stdio 均默认开启；显式关闭过的设置继续保留，系统设置可重新启用。扫描纳管使用通用 command / args / env / cwd 配置，不因某个 Server 名称切换专用启动器。

Node.js、Python 等 MCP 运行依赖不打进安装包；页面提供实际环境检测。桌面启动会补充常用运行时目录到 PATH，不执行用户 shell 初始化脚本。

“系统设置 → 下载备份”使用原生保存对话框，按本机管理员权限导出真实 SQLite 一致性快照；取消对话框不会写文件。恢复备份仍需要原来的 `local.env` 主密钥，备份数据库不包含安装制品目录。

## 开机启动与退出

0.1.2 起，在个人版“系统设置 → 开机启动”可开启或关闭登录 macOS 自动启动。开启后创建当前用户的 LaunchAgent，并自动把完整应用保存到本地数据目录的 autostart/ 下，避免 App Translocation 或下载临时路径失效。配置按数据目录隔离，重启保留；更新应用时自动刷新已开启的启动副本。关闭不会退出当前应用，下次登录不再自动启动。

关闭窗口继续后台运行；应用菜单、Cmd+Q 和系统退出请求均通过原生终止流程正常退出，不再被隐藏窗口拦截。

## 分发边界

当前 macOS 构建为 ad-hoc 本地签名，未做 Apple Developer ID 签名与公证。网页明确展示该状态；不能把它描述为已通过 Gatekeeper 的正式企业分发包。面向其他电脑正式分发前，应使用企业开发者证书签名并完成公证，再发布包与校验清单。

使用 `Toolbox.app/Contents/MacOS/Toolbox --data-dir /绝对路径` 可以创建独立本机配置，用于真实场景验收；不要让两个进程共用一个 SQLite 数据目录。

## Windows 11 构建

macOS/Linux 在 code 目录执行 `TOOLBOX_GO_BIN=/绝对路径/go ./scripts/build-desktop.sh windows-amd64`。Windows 主机执行 `powershell -ExecutionPolicy Bypass -File .\scripts\build-desktop-windows.ps1`。两者都生成真实的 `desktop/build/bin/Toolbox.exe` 以及 `dist/desktop/Toolbox-版本-windows-amd64.zip`，包中含 Windows 使用说明，清单记录实际 SHA256 与最低内部版本 22000。

Windows 包当前未做 Authenticode 签名。交叉编译成功只证明生成真实 PE x64 文件，Windows 11 实机启动、原生文件选择、stdio、退出、开机启动、升级数据保留仍需目标环境验收。
