# 企业版软件包管理与配置

管理接口仅允许企业版管理员访问；个人版与普通用户拒绝。上传文件不执行。

| 接口 | 行为 |
|---|---|
| GET /api/v1/system/packages | 返回全部版本及文件完整性 |
| POST /api/v1/system/packages | multipart：file、version、os、arch、min_os、notes；最大512MiB；ZIP或Windows EXE；默认草稿 |
| PATCH /api/v1/system/packages/:id | status 为 published、draft、archived；发布前重新校验制品 |
| DELETE /api/v1/system/packages/:id | 归档，保留文件及版本，可恢复发布 |
| GET/PATCH /api/v1/system/package-settings | directory、editable；目录位于服务所在电脑，空库可通过页面切换；已有版本需先迁移完整目录后改配置并重启 |
| GET/PATCH /api/v1/system/server-config | 返回 path、values、environment_overrides、restart_required；PATCH 接受 values 中的完整JSON配置对象，原子保存后重启生效 |
| GET /api/v1/desktop/releases | 公开已发布且摘要正确的版本；支持os、arch筛选；兼容旧manifest |
| GET /downloads/desktop/:filename | 公开下载发布制品，再次校验大小和SHA256，支持HTTP Range |

安装包身份由版本、操作系统、架构共同决定；重复拒绝覆盖。上传检查ZIP路径、成员CRC和解压体积限制，并根据PE/Mach-O/ELF实际头部校验目标平台和架构。Windows最低版本为11。软件包与index.json原子写入配置目录，索引和制品权限0600；下载不信任上传者自报签名，未验证开发者签名时展示unsigned。

企业程序默认读取可执行程序同目录toolbox.json；不存在时创建。可用 --config /absolute/path/toolbox.json 或 TOOLBOX_CONFIG_FILE 指定位置。配置示例见 code/config/toolbox.example.json。相对路径以配置文件目录为基准，优先级为命令行监听参数 > 对应环境变量 > 配置文件；默认端口8090。环境变量：LISTEN_ADDR、SQLITE_PATH、TOOLBOX_DESKTOP_RELEASES_DIR、PUBLIC_URL、ACCESS_CIDRS、TLS_CERT_FILE、TLS_KEY_FILE。

端口页面立即切换时同步保存toolbox.json；软件包保存目录页面切换时同样同步配置文件。配置文件页保存的其余设置待服务重启，页面展示待生效状态。文件被外部改动时拒绝用旧页面覆盖。密钥继续首次自动生成并存于私有数据目录，不要求在配置文件中填写。

此功能管理版本分发与下载，不会自动替换正在运行的企业服务器或强制升级用户桌面程序。用户下载新版本后退出旧桌面再替换程序，本机数据目录保留。Windows签名和Windows11实际运行需单独验证，PE交叉编译通过不代表实机验收通过。
