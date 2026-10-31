# v1.2.0 Windows WSL2 Appliance 迁移说明

> 历史记录：以下内容保留当时的实现、命令与验收语境，不代表当前发布状态。当前指南见[文档导航](README.md)，不要直接用本页旧命令安装或发布 2.0。
> 下文路径基于历史 1.x 源码树；完整旧版请按[维护分支说明](legacy/README.md#维护-1x)独立检出，不能直接在 main 或归档目录执行。

## 发生了什么变化

Windows Electron 桌面版不再启动 Docker Desktop 或 Compose。它会导入并管理固定
名称 `AppleMusicDownloader` 的专用 WSL2 发行版，再加载发行版内同一套 Web UI。
Docker Web、Go CLI、Job API、SQLite 队列以及“删除任务和该任务登记文件”的语义
没有改变。

旧桌面版的 Docker 数据不会自动迁移进 WSL VHDX，以免在未经确认时复制凭据或
媒体。原 Docker `downloads/`、`data/` 和 `wrapper-data/` 会原地保留，Docker
Compose 入口仍可继续使用。

## Docker Web 凭据与 API 迁移

v1.2 的 Compose 不再使用 `env_file`，也不再把 `APPLE_EMAIL`、
`APPLE_PASSWORD`、`MEDIA_USER_TOKEN`、`AUTHORIZATION_TOKEN` 或
`AMDL_API_TOKEN` 展开到容器环境。升级已有 Docker 部署时：

1. 保留受 Git 忽略且权限受限的 `.env`，并加入或保留
   `AMDL_DOCKER_CREDENTIALS_FILE=./.env`。也可以把该非秘密路径指向另一份受
   保护的 dotenv 文件。
2. 删除自定义 Compose 中的 `env_file` 以及上述秘密的 `environment` 映射。
   新 Compose 只把 `AMDL_CREDENTIALS_FILE=/run/secrets/amdl-credentials.env`
   这个非秘密路径放入 `Config.Env`，并将宿主凭据文件只读挂载到该位置。
3. 没有凭据时无需创建 `.env`：默认挂载无秘密的 `.env.example`，Web、健康检查
   和能力接口仍可启动。若显式设置了 `AMDL_DOCKER_CREDENTIALS_FILE`，目标文件
   必须已经存在，Compose 不会代为创建。

`.env` 仍可供 Compose 解析宿主下载、Wrapper 状态目录和本机端口，但秘密值不会
被引用进服务环境。不要把凭据复制进镜像、`docker-compose.yml` 或诊断输出。

Docker Desktop 无法在 Windows bind mount 上向 Linux 容器保留可靠的 Unix
owner/mode；v1.2 因此把 `/app/data` 改为 Compose 管理的私有 `amdl-data` volume。
旧版 `./data` 不会被删除或自动导入，因为在宽松 bind mount 上打开预植 SQLite
会绕过新的安全边界。需要保留旧任务历史时，请先停止旧容器并在受控环境中审核、
备份后手工迁移 `jobs.db`；正式下载仍位于 `downloads/`，不受此变化影响。

Job API 同时改为仅接受 Bearer 访问密钥。除静态 Web 资源和
`/api/v1/health/live`、`/api/v1/health/ready` 外，所有 `/api/` 请求（包括 SSE、
文件下载、日志、诊断和任务删除）都必须携带
`Authorization: Bearer <64位十六进制密钥>`。`/api/v1/session` 已移除，旧
`amdl_session` Cookie 不再授权且会被清除；密钥也不得放入 Cookie、URL 或查询
参数。未设置 `AMDL_API_TOKEN` 时，Docker/native serve 会在持久数据目录的
`api-access-token` 文件中生成权限受限的密钥。旧脚本和自行维护的客户端必须在
升级时改用 Bearer 请求头，否则会收到 `401 api_auth_required`。

## 首次启动 v1.2 桌面版

1. 安装 `AppleMusicDownloader-Setup.exe`。
2. 让向导检查或启用 WSL2。执行官方安装或更新前，页面会说明将启用的 Windows
   功能；确认后弹出 UAC，并打开可见的管理员终端显示固定命令进度。请保持终端和
   向导开启直到命令结束；失败或主动关闭时可直接在向导中重试。需要重启时，登录
   后会继续设置。
3. 选择正式下载目录。若要继续使用原 Docker 下载目录，可选择该目录，但已有
   文件只会索引为 unmanaged，不会移动、改名或删除。
4. 重新保存凭据。不要把旧 `.env` 复制进 WSL；桌面版使用 Windows DPAPI 与
   一次性 stdin 凭据通道。

## 数据位置与回退

- Appliance 数据库：`/var/lib/applemusic-downloader/data/jobs.db`
- Wrapper 状态：`/var/lib/applemusic-downloader/wrapper-state`
- 运行时更新终态回执：`/var/lib/applemusic-downloader/update-receipt.json`
- 版本化运行时：`/opt/applemusic-downloader/releases/<version>`
- Windows 正式下载：默认 `%USERPROFILE%\Music\Apple Music Downloader`

卸载桌面应用时默认保留专用发行版/VHDX。只有在卸载提示中明确选择删除任务数据
才会执行 `wsl --unregister AppleMusicDownloader`；正式下载始终保留。普通更新
和“修复运行时”绝不使用 unregister。

要回退到 Docker，只需停止桌面 WSL 服务并继续使用根目录 Compose。不要同时让
Docker 和 WSL Appliance 写入同一个下载目录。

## 发布限制

v1.2 源码允许本地 PoC 和私有构建。当前 Wrapper、Android/Apple 库和 CDM 材料
的来源、许可证与再分发授权尚未全部验证，因此公开 rootfs、组合镜像或安装包的
发布工作流会硬失败。详见根目录 `NOTICE` 与 `appliance/release-policy.json`。
`local-poc`、`private-ci` 和 `private-build` 仅用于受控验证，不代表取得了再分发
权；在门槛解除前，不得把这些组合产物上传到 Release、公共 CI artifact、公共
容器仓库，或分发到获授权的私有验证范围之外。环境变量不能绕过仓库内的发布
策略。
