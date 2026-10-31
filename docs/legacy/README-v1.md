# 1.x 历史说明（非 2.0 安装指南）

此页保留整理前的 README。命令中的路径以当时的 1.x 仓库根目录为基准，不能直接在当前 main 执行；
需要维护旧版时见[独立检出说明](README.md#维护-1x)。当前版本请从[2.0 首页](../../README.md)进入。

# Apple Music Downloader

面向本地使用的 Apple Music 下载器。Windows 普通用户使用专用 WSL2
Appliance 桌面版；服务器和高级用户仍可使用 Docker Web 或 Go CLI。三种入口
复用同一套 Go 后端、SQLite 持久化队列、Web UI 与 Linux Wrapper，不维护独立的
Windows 下载逻辑。

> 请只下载你有权访问、保存和使用的内容，并遵守 Apple 的服务条款及当地
> 法律。凭据、Token、`wrapper-data/` 和下载文件均属于敏感本地数据。

当前 `appliance/release-policy.json` 禁止公开发布组合镜像、WSL rootfs/runtime
和 Windows 安装包。下述桌面安装步骤仅适用于受控的本地或私有测试构建；本仓库
没有可供公众下载的安装包。来源见[上游清单](../../docs/UPSTREAMS.md)，许可与授权门槛
见 [NOTICE](../../NOTICE)。

## 入口与支持范围

- **Windows 桌面版（Windows 用户推荐）**：只需运行
  `AppleMusicDownloader-Setup.exe`。安装向导自动准备名为
  `AppleMusicDownloader` 的专用 WSL2 发行版，不依赖 Docker Desktop，用户
  不需要终端、Compose、`.env` 或 Linux 知识。
- **Docker Web**：服务器和高级用户的完整本地入口，使用本仓库根目录的
  `Dockerfile` 与 `docker-compose.yml`。
- **Go CLI**：高级用户/脚本入口，源码及其命令说明位于
  `apple-music-downloader/`；它与 Web 服务使用同一下载引擎与配置。

Go CLI 与 Docker 模式继续受支持；根目录 `start.bat` 仍是旧式 Docker Web
启动助手，不是 Windows 桌面安装器。

## Windows 10 / 11：普通用户安装

桌面版兼容目标为 **Windows 10 21H2/22H2（Build 19044+）或 Windows 11 x64**，
Windows 10 建议使用已安装系统更新的 22H2。32 位、ARM64 和更早的 Windows
不在支持范围。Windows 10 的版本门与模拟回归已覆盖，实机安装验收仍待完成，
详见 [Windows 兼容性与验收](../../docs/WINDOWS_COMPATIBILITY.md)。CPU 固件虚拟化和新版 WSL2 必须可用，但不
要求安装 Docker Desktop。在线安装包会下载经过 SHA-256 锁定的 rootfs 和
runtime；离线安装包内嵌并校验同一组 rootfs 与 runtime。

1. 从受控的私有构建中取得对应版本的安装包，先核对构建记录中的 SHA-256，
   再运行安装包。若安装包未签名，Windows SmartScreen 可能显示“未知发布者”。
   使用 `local-poc` 的在线安装包只适用于构建它的机器，因为 manifest 指向
   本机 `file://` 产物路径。
2. 向导会检测 Windows、x64、虚拟化、WSL 与 WSL2。需要 Windows 官方安装或
   更新时，向导只执行固定的 `wsl --install --no-distribution` / `wsl --update`
   操作。点击前会说明将启用 WSL 与虚拟机平台；点击后先弹出 UAC，再打开可见的
   管理员终端显示官方命令进度，向导同时提示“请勿关闭窗口”。命令结束后会明确
   显示成功、失败或需要重启，并可在登录后继续。
3. 默认正式下载目录是 Windows 已知“音乐”目录下的
   `Apple Music Downloader`。可在向导中选择其他本机 NTFS/ReFS 目录；UNC、
   设备路径和相对路径会被拒绝。
4. 凭据可在向导中填写或稍后从“设置 → 配置凭据”设置（“服务”菜单保留备用入口）。它们使用 Windows
   DPAPI 加密，传入 WSL 时只经过一次性 stdin；不会生成明文 `.env`。
5. 后端健康后自动打开同一套 Web UI。关闭桌面窗口不会终止 WSL 服务或下载；
   显式“停止服务”会在存在活动任务时再次确认。

专用发行版固定安装到 `%LOCALAPPDATA%\AppleMusicDownloader\wsl`，不会修改其他
WSL 发行版、默认发行版或全局 `.wslconfig`，也不会调用 `wsl --shutdown`。

### Windows 故障恢复

- `wsl_missing` / `wsl_update_required`：使用向导中的官方安装或更新按钮。
  如果取消 UAC、关闭管理员终端或官方命令返回失败，向导会显示中文原因并保留
  同一个重试按钮；不需要手工打开 PowerShell。
- `virtualization_disabled`：在 BIOS/UEFI 中启用 Intel VT-x 或 AMD-V 后重启。
- `reboot_required`：保存工作并用向导重启；登录项会带 `--resume-setup` 继续。
- `port_in_use`：应用会优先选择另一个仅绑定 `127.0.0.1` 的空闲端口。
- `distro_corrupt`：先使用“修复运行时”；普通修复和更新不会 unregister。
- `health_timeout`：向导最多等待 180 秒，并显示发行版状态与最近 200 行脱敏
  日志。不要通过手工 unregister 作为第一步排错。

## Docker Web：服务器与高级用户

### 前提条件

- Docker Desktop（Windows/macOS）或 Docker Engine（Linux），以及
  **Docker Compose v2.24 或更高版本**。
- Docker 已分配至少 4 GB 内存，并允许特权容器运行；wrapper 需要此能力。
- 容器和 wrapper 目前只支持 **linux/amd64**。ARM Linux、Apple Silicon 和
  Windows ARM 不在本版本支持范围内。
- 端口 `8080` 可用。它仅绑定到 `127.0.0.1`；wrapper 的 `10020/20020`
  仅在容器内部使用，绝不发布到主机。

### 五分钟启动

`.env` 是可选的。没有 Apple 凭据时服务仍会启动；本地 Job API 默认需要一枚
随机访问密钥，以防同机其他进程读取日志/媒体或删除任务文件。未设置
`AMDL_API_TOKEN` 时，服务会在 `data/api-access-token` 自动生成一个权限受限的
密钥，不会把值写进日志、URL 或进程参数。Compose 不再把 `.env` 的秘密写入
容器 `Config.Env`：它只读挂载该文件，Go 服务启动时直接解析到内存；缺少
`.env` 时改挂载无秘密的 `.env.example`，服务照常启动。

1. 仅在需要设置凭据、Token 或本地目录时复制示例：

   ```bash
   cp .env.example .env
   ```

   Windows PowerShell：

   ```powershell
   Copy-Item .env.example .env
   ```

2. 需要的情况下编辑受 Git 忽略的 `.env`，保留
   `AMDL_DOCKER_CREDENTIALS_FILE=./.env`，不要把秘密直接写进
   `docker-compose.yml`。保护该文件，使其他本机用户无法读取。

3. 构建并等待服务健康：

   ```bash
   docker compose up -d --build --wait --wait-timeout 180
   ```

4. 打开 [http://127.0.0.1:8080](http://127.0.0.1:8080)。首次访问 API 时，
   浏览器会要求本地访问密钥。建议在 `.env` 中设置 `AMDL_API_TOKEN`；若留空，
   可用 `docker compose exec app /bin/cat /app/data/api-access-token` 读取私有
   `amdl-data` volume 内自动生成的密钥。密钥只保存在当前页面的 JavaScript
   内存中，并通过 `Authorization` 请求头访问 API、事件流和文件；刷新或关闭
   页面后即清除，不写 Cookie、URL 或浏览器持久存储。wrapper 会自动启动；若
   页面提示双重验证，请在页面中提交六位验证码。Docker Web 用户下载大文件或
   任务 ZIP 时请使用最新版 Chrome/Edge：页面会通过系统“另存为”窗口直接流式
   写盘；不支持该接口的浏览器只对已知小于 128 MiB 的响应启用内存回退。

需要继续使用 Docker 的 Windows 高级用户可执行 `start.bat`。它会检查 Docker、
最多等待 180 秒、在失败时显示 Compose 状态和最近 200 行日志。

停止服务不会删除数据：

```bash
docker compose down
```

## 凭据与配置

Docker 用户使用只读挂载的 `.env`，CLI 用户使用环境变量或显式 YAML；Windows
桌面版在系统加密存储中保存同名能力所需的值，不写 `.env`。通常只需填写实际
需要的项目：

| 变量 | 用途 |
| --- | --- |
| `APPLE_EMAIL` / `APPLE_PASSWORD` | wrapper 支持的 ALAC、AAC 和 Atmos 音质；首次登录可能需要 2FA。 |
| `MEDIA_USER_TOKEN` | AAC-LC 和歌词所需。 |
| `AUTHORIZATION_TOKEN` | 可选 API 覆盖；留空时下载器会在支持时自行获取。 |
| `STOREFRONT` | 两位地区代码，例如 `us`、`jp`、`cn`。 |
| `AMDL_API_TOKEN` | 可选的 64 位十六进制本地 API 访问密钥；留空时在持久数据目录自动生成。 |
| `AMDL_DOCKER_CREDENTIALS_FILE` | Compose 要只读挂载的受保护 dotenv 文件；复制示例后保持为 `./.env`。 |
| `DOWNLOADS_DIR` / `WRAPPER_DATA_DIR` | 宿主机下载与 Wrapper 状态目录，可保留默认相对路径。 |
| `AMDL_HOST_PORT` | Docker Web 的本机端口，默认 `8080`，始终只绑定 `127.0.0.1`。 |

配置优先级为：**CLI 参数 > 环境变量 > 显式 YAML 配置 > 内置默认值**。Docker
容器内部固定使用 `/app/working/config.yaml`、`/app/downloads` 与 `/app/data`。
下载和 Wrapper 状态可通过 `.env` 调整宿主机目录；任务数据库和本地 API 密钥
固定保存在权限可验证的 `amdl-data` Docker volume。Docker Desktop 的 Windows
bind mount 在 Linux 容器内呈现宽松权限，因此不能用于 `/app/data`。

### Go CLI

在 `apple-music-downloader/` 目录中运行 Go CLI。常用命令如下：

```bash
go run . download URL... --quality alac --existing skip --max-retries 2
go run . search album "query"
go run . serve --port 8080
go run . doctor --json
go run . config show --redact
```

使用 `--config FILE` 指定 YAML 配置，或设置 `AMDL_CONFIG`。Apple Token 不再
接受 `--media-user-token VALUE` / `--authorization-token VALUE`，避免进入 shell
history 和进程命令行；请改用权限受限的显式配置文件或环境变量。两者都不存在时，
v1.1 仍会兼容读取当前目录的 `config.yaml`，但会输出弃用警告；请在 v2.0 前
迁移到前两种方式。`--server`、`--search`、`--aac`、`--atmos` 等旧参数在 v1.1
仍会映射到新命令并向 stderr 输出弃用警告。

CLI 退出码固定为：`0` 成功或跳过，`1` 运行或部分失败，`2` 参数/配置错误，
`130` 用户取消。`config show` 必须带 `--redact`，不会显示 Token。

v1.3 的持久化队列接受专辑、公开歌单、个人资料库歌单、单曲、音乐视频和电台链接。
个人资料库歌单沿用已配置的 `Media-User-Token`，服务会优先使用 Apple Music API
分页读取；公开页面只作为受限结构化数据兜底，不读取浏览器 Cookie。Artist 链接的
旧式交互展开不属于 Web/Job API/`download URL...` 命令的支持范围，因此会在
入队前返回参数错误，而不会创建一个随后失败的任务。

任务队列一次执行一个 Job；Job 内曲目下载默认并行数为 3，可在 Web 设置中调整
为 1–8。失败曲目的自动重试不会重复下载同一任务中已成功的曲目。

不要在 URL、截图、Issue、日志包或共享的 Compose 输出中包含 Token、密码或
Apple 会话数据。`wrapper-data/` 可能保存登录状态，也应按凭据处理。

## 数据保留与诊断

Docker Compose 使用以下持久化位置：

- `downloads/`：正式下载结果。服务不会按文件年龄自动删除这里的媒体；只有用户明确确认“删除任务和文件”时，才会删除该任务登记的输出。
- `amdl-data` Docker 命名卷（容器内 `/app/data`）：任务数据库、状态及 `api-access-token`。删除任务和文件会一并移除对应的任务元数据和安全文件 ID；未托管或跳过的已有文件保持不变。
- `wrapper-data/`：wrapper 登录和设备状态。

临时文件只在数据库明确登记为临时项时清理。升级、停止或重启容器不会移走、
改名或删除已有下载。

Windows Appliance 的对应位置为：

- 正式下载：默认 `%USERPROFILE%\Music\Apple Music Downloader`，永久保留。
- SQLite/WAL：发行版内 `/var/lib/applemusic-downloader/data/jobs.db`。
- Wrapper 登录/设备状态：发行版内
  `/var/lib/applemusic-downloader/wrapper-state`。
- 最近一次运行时更新的非秘密终态回执：
  `/var/lib/applemusic-downloader/update-receipt.json`。它以 `0600` 原子写入，
  绑定 source/target runtime 摘要和事务 ID，使 supervisor 重启后仍能区分提交与
  回滚；损坏、权限错误或身份冲突会停止自动协调并要求修复。
- 桌面非秘密进度与 DPAPI 密文：
  `%LOCALAPPDATA%\AppleMusicDownloader`。

当前 Windows Appliance 只接受全新安装或同版本 repair。检测到另一已安装版本时
返回 `runtime_reinstall_required`，不会原地覆盖旧发行版或迁移数据。同版本 repair
只替换 `/opt/applemusic-downloader/releases/<version>` 并原子切换 `current`；每个
安装后的 release 记录 runtime SHA-256 与事务 ID，以区分提交和回滚。Windows
端只有在 host manifest/runtime 绑定、当前 release 身份和上述持久终态回执全部
匹配时，才会清除 rolled-back host 事务；单凭版本相同、等待超时或 source 文件
仍在绝不推断回滚；
`/var/lib` 与 Windows 下载目录不属于 release。卸载默认停止服务并保留发行版
数据；只有明确选择“删除任务数据”才 unregister。两种卸载选择都保留正式下载。

常用检查命令：

```bash
docker compose ps
docker compose logs --tail 200 app
curl --fail http://127.0.0.1:8080/api/v1/health/live
curl --fail http://127.0.0.1:8080/api/v1/health/ready
api_key="$(docker compose exec -T app /bin/cat /app/data/api-access-token | tr -d '\r\n')"
printf 'header = "Authorization: Bearer %s"\nurl = "http://127.0.0.1:8080/api/v1/capabilities"\n' "$api_key" | curl --config -
unset api_key
```

Wrapper 日志会在脱敏后同步输出到 `docker compose logs`，状态接口
`/api/wrapper-status` 保留最近 200 行。启动失败时，先运行
`docker compose config --quiet`，再检查上述日志；不要为了“重试”而删除
`downloads/`、`data/` 或 `wrapper-data/`。

## 更新与安全边界

更新本地源码后可重新执行启动命令：

```bash
docker compose up -d --build --wait --wait-timeout 180
```

Web 服务默认只对本机开放。若你决定通过反向代理或修改端口映射让其他设备访问，
必须自行添加认证、TLS 与网络访问控制；远程 Web 暴露不属于本版本支持范围。

构建镜像会从根仓库内的 `apple-music-downloader/` 源码开始，先下载已锁定的
Go 模块、执行测试，再编译；构建过程不会运行 `go mod tidy`。CI 同时验证
Compose 可在没有 `.env` 的全新克隆中解析。

### 维护者：桌面开发与 DRM 验收

桌面开发入口需要 Windows x64 和 Node.js 22。Electron 使用 `package.json`
声明的固定 Castlabs ECS 源码归档；`package-lock.json` 保存安装完整性记录，
运行时版本与目录约定位于 `electron/drm-runtime.js`。普通 Electron 不能替代它。

`npm ci` 安装依赖后，使用 `npm start` 启动。若旧开发进程占用了原安装目录，
先关闭应用，再运行 `npm run install:drm`：它从同一声明来源安装到独立的
`node_modules/.amdl-drm`，禁用自动生命周期脚本后只执行固定包的安装器一次。
启动和打包优先选择这个完整且版本匹配的目录，再检查普通安装目录；都不匹配时停止。
安装命令不修改项目依赖声明或锁文件。

`npm test` 只发现 `test/` 下的 `*.test.js`，`npm run test:electron`
只执行 Electron 的 Node fixtures；两者都不运行真实 DRM/CDM 验收。
需要单独验证已安装运行时时执行：

```powershell
npm run smoke:drm
```

这个入口可能联网下载或初始化 Widevine CDM。它使用新建临时 profile、合成 Token
和本地 fixture 页面，最多运行 90 秒，退出后只清理自己的临时目录；不读取已有应用
profile，不登录 Apple，不启动 WSL，也不下载媒体。成功只证明 CDM、EME、系统加密和
合成 Cookie 流程可用，不代表订阅或真实歌曲播放获准。失败输出包含有界的阶段代码。
需要将临时文件留在项目内时，可将 `TEMP`/`TMP` 指向已有的专用临时目录。

打包前还需匹配当前桌面版本的私有 Appliance manifest。构建保留上游 DRM EXE、
detached signature 和完整性 fuse 约束；公开发布仍受现有策略阻止。详见
[发布流程](../../docs/RELEASE.md)。

### 维护者：构建 WSL Appliance

Windows 普通用户不需要执行本节。维护者可在 amd64 Linux，或 Windows 11 x64
的 **Git for Windows Git Bash + Docker Desktop（Linux containers）** 中构建。
不要从 PowerShell、`cmd.exe`、Windows 的 WSL `bash.exe` 或 UNC 路径直接运行
这些 Bash 脚本；也不要全局设置 `MSYS_NO_PATHCONV`。Git Bash 路径包含空格、
中文时应始终使用引号。

正常本地私有构建使用已审查的锁文件，不要先自动刷新锁。构建器先生成 runtime，
再把该 runtime 的 SHA-256 与确定性初始事务 ID 写入 rootfs 的 release identity：

```bash
./appliance/scripts/build-all.sh --channel local-poc
./appliance/scripts/verify.sh --dist appliance/dist --require-artifacts
```

打包时，宿主机只保存锁定的 BuildKit context、`docker export` 流和最终产物；
解包、清理运行状态、规范 UID/GID/权限/mtime、排序 tar 与 `gzip -n` 全部在一次性
Linux 容器中完成，避免 NTFS/MSYS 元数据进入 rootfs。发布候选还必须完成两次
独立无缓存构建：

```bash
./appliance/scripts/verify-reproducible.sh --channel private-ci
```

`lock-local-inputs.sh` 只在有意更新源码输入后运行，
`resolve-packages.sh --accept` 只在审查 `packages.lock` 变更后运行；两者生成的 diff
都必须与对应来源变更一起评审。当前 `appliance/release-policy.json` 会硬性拒绝
`--channel public`。Wrapper、Android/Apple/CDM 的来源、许可证与再分发授权未补齐
前，不得公开上传私有 rootfs、runtime、组合镜像或安装包。完整依赖、路径和门槛
见 [Appliance 构建说明](../../appliance/README.md) 与 [发布流程](../../docs/RELEASE.md)。

## 许可证与来源

两条核心上游及固定构建提交见[上游清单](../../docs/UPSTREAMS.md)。关于内嵌下载器、
Wrapper 的来源及第三方许可证提醒，请阅读 [NOTICE](../../NOTICE)。
各组件内保留的许可证文件仍然适用于对应内容。

升级旧版本请阅读 [v1.1 迁移说明](../../docs/MIGRATION-v1.1.md) 与
[v1.2 WSL2 迁移说明](../../docs/MIGRATION-v1.2.md)；维护者构建 rootfs、runtime、镜像、
CLI 和 Windows 安装包时请遵循 [发布流程](../../docs/RELEASE.md)。
