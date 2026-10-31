# 2.0.x：无 WSL、无内置 Apple 网页

> 历史记录：以下内容保留当时的实现、命令与验收语境，不代表当前发布状态。当前指南见[文档导航](../README.md)，不要直接用本页旧命令安装或发布 2.0。

2026-10-25 当前本地安装包为 `2.0.0`，已包含 ALAC 普通 M4A 兼容封装与完整标签保留，版本和显示名称去掉 Beta/Test；沿用旧资料身份以保留账号和任务。公开发布限制保持不变，没有公开上传。最终安装包解出的应用已完成隔离启动检查，见 [2.0.0 本地包记录](../testing/2026-10-25-v2.0.0-local-package.md)。以下测试包说明为历史记录。

2026-10-23 最新测试包：`...-a83ca03a-8b28-4dfc-a16d-be0ee2dcac6f` 修正 Lite token 到官方 Music API 的请求头衔接，补齐 401/403 与白名单 Apple 错误码诊断；不再将 developer 授权拒绝一律视作账号登录失败。保留 Lite 原有设备验证码登录，不恢复短信、GSA/ADI 专用执行链路或独立 helper，也不删除已有账号、会话或诊断历史。新包通过包内审计及最终包解出程序的空账号启动、合成错误导出验证，见 [本次校验修复](../testing/2026-10-23-native-music-handoff.md)。此前设备验证码专用包 `...-4922bcdb-...` 与短信里程碑均保留为历史记录；真实账号登录仍待用户测试。

状态：2026-10-22 已生成私有 `2.0.0-beta.1` Windows x64 NSIS 测试包，完成包内摘要审计及从最终包解出的应用的空账号启动验证。自有登录窗口、原生任务后端、Lite 音频下载适配和本地播放器已接入。真实账号/媒体、实际安装/卸载和无 WSL 干净目标机尚待用户验收。下文早期里程碑保留为历史记录，以末尾最新证据为准。

## 已确认的目标

- 从最新干净的 1.4 分支提交 `c0719fc61b7a553348e8d7be96321cc37eb0a0d9` 开发。
- 独立工作树：`.worktrees/v2-native-desktop`；分支：`codex/v2-native-desktop`。不覆盖 1.4 的代码、安装包、账号或下载文件。
- Windows 安装和运行不要求 WSL、WSL 发行版或 Docker。构建环境与最终用户运行依赖分别核验。
- 不再内置 Apple Music 网页，包括隐藏的官方网页播放器、网页登录窗口、Cookie 抓取和网页下载按钮注入。
- 这里的“原生 UI”采用应用自己绘制的界面，沿用本地 Electron renderer；不是承诺改用 WinUI，也不是继续显示 Apple 网页。
- 保留目录搜索、推荐、专辑/歌单详情、个人歌单、下载任务、设置、日志导出及播放操作入口。每项功能的可用状态必须来自对应能力的验证。

## 建议运行结构

| 层 | 2.0 职责 | 不再使用 |
| --- | --- | --- |
| 本地桌面界面 | 自有目录、详情、任务、设置、登录和播放器 UI | Apple 网页、WebContentsView、webview、MusicKit 网页组件 |
| Windows 后端 | 原生 EXE；目录/API、任务数据库、下载文件、进程与诊断管理 | WSL supervisor、DrvFS 路径转换、发行版修复 |
| Wrapper 适配 | Wrapper Lite HTTP 协议、生命周期、账号挑战与能力检查 | 旧 Wrapper TCP 协议的直接复用 |
| 兼容运行时 | 捆绑 QEMU 与受版本锁约束的 guest；TCG 为无硬件虚拟化的候选基线 | 用户安装 WSL 或 Docker |

QEMU 方案仍包含 guest 运行时，不是把 Android Wrapper 直接编译成 Win32 EXE。TCG 的启动、耗时、稳定性必须在目标 Windows 上验证；WHPX 只能作为可选加速，不得静默打开系统功能。若 TCG 无法满足验收，须报告限制，不得悄悄恢复 WSL。

本地 renderer 本身由 Electron 承载；“无内置浏览器”在此指无远端网页视图及网页功能依赖。测试包已切换到精确锁定的标准 Electron `45.0.0`，不再选用 DRM 发行版。

## 现有代码需要迁移的位置

- `src/main/main.js`：当前同时导入 `WslManager`、`createAppleMusicBrowser`、`createOfficialWebPlayer`；2.0 改为原生后端生命周期，不创建远端页面或登录窗口。
- `src/preload/preload.js`：移除 `desktop:music-*` 网页操作及网页登录 IPC；播放、账号和状态只暴露固定本地业务动作。
- `src/preload/apple-music-preload.js`、`electron/apple-music-browser.js`、`electron/official-web-player.js`：不进入 2.0 运行入口和安装包；对应旧测试随替代功能完成后移除，不通过忽略失败掩盖回归。
- `src/renderer/catalog.js`、`src/renderer/playback.js`、`src/renderer/index.html`、`src/renderer/browse.js`：保留自有目录，移除网页模式及隐藏播放器路径；无播放能力时明确提示，不伪称完整播放成功。
- `electron/wsl-manager.js`、`electron/setup-controller.js` 及 WSL 专用向导：由原生运行时管理替代；不得用改名后的类继续调用 `wsl.exe`。
- `apple-music-downloader/jobs_process_executor.go`、`process_secret_fd_windows.go`、`process_tree_windows.go`：复用现有 Windows 子进程、stdin 凭据和任务回执机制，验证完成回执、取消和文件落盘的顺序。
- `apple-music-downloader/internal/appliance`：不作为 Windows 原生 supervisor 直接套用；Linux mount/chroot 路径不能进入 Windows 文件操作。
- `apple-music-downloader/utils/runv2`、`utils/runv3`：新 Lite 适配需要独立协议与解密兼容验证，不能只把 TCP 端口改成 HTTP。
- `electron-builder.base.cjs`、`package.json` 和版本锁/发布脚本：最终安装包切换为原生 EXE、QEMU/guest 和新 manifest；移除 WSL rootfs/runtime 作为安装前提。

## 必须先完成的技术验证

### A. 原生运行时

1. 固定 Wrapper Lite 源码版本与构建输入；首先评估已查阅的 `2fd7e0eaef081f93cf687428b965674937832ba4`，不能随 `lite` 分支漂移。
2. 无凭据启动 QEMU/guest，验证 TCG、退出、崩溃恢复、端口占用、路径含空格/中文、Windows 重启后的状态持久化。
3. 服务状态区分宿主进程、guest 就绪、Lite HTTP 就绪。`/status` 的缓存地区不能用作账号已实时登录的证据。
4. Windows Job Object 管理完整子进程树；取消/退出须先完成日志读取，再释放进程和 IPC，不重现 `file already closed`/对象已销毁竞态。

### B. 自有登录 UI 与个人数据

1. 账号、验证码由应用自有窗口提交给固定后端动作；恢复缓存优先，只有会话明确失效才重新挑战。
2. 上游 `--login user:pass` 不能成为最终凭据通道。先定义并验证 stdin/受控 pipe 到 guest 的传输，密码不进入 argv、日志或普通任务状态。
3. `MUSIC_TOKEN`、developer token 和 Web `Media-User-Token` 分开建模；不得仅按名称把它们互换。
4. 验证 Lite 登录所得凭据是否能合法调用目录所需 API、`/me/library/playlists`、个人推荐及全部分页。浏览/个人歌单验证通过前不得标记这些能力可用。
5. 如果 Lite 不能直接满足个人资料库访问，需选择受支持的授权路径并报告与“无内置网页”的约束是否冲突；不偷偷恢复 Cookie 提取或网页窗口。

### C. 下载与完整播放

1. 以固定 HTTP fixture 验证 `/m3u8`、`/key`、`/lyrics`、`/webplayback`、`/license` 的实际合同；旧样本解密协议不兼容时返回固定错误。
2. 对兼容引擎先做无账号的合成媒体测试，再由用户单独授权真实账号验收；不能从现有本机凭据自动发起媒体请求。
3. 自有播放器通过新后端能力播放，不嵌入官方网页、不再依赖隐藏网页 DRM。解码、音频输出、暂停/切歌/定位、结束和资源释放均单独验收。
4. “播放”仍为单入口。完整播放不可用时可以使用可用试听，但必须显示“试听”，不能用试听通过完整播放验收。
5. UI 分别反映目录、登录、个人资料库、指定音质下载、完整播放能力；绿灯只对已验证的操作有效，不保证下一首内容有权利或许可。

## 迁移顺序与交付闸门

| 阶段 | 交付 | 通过证据 |
| --- | --- | --- |
| 0 | 架构能力合同、独立工作树、1.4 基线记录 | Node 基线结束状态和新合同测试 |
| 1 | Windows 原生后端 + Lite/QEMU 启停原型 | 未启用 WSL 的测试机启动、停止、故障日志；无账号 |
| 2 | 自有登录与独立凭据能力验证 | 恢复/2FA/失败 fixture；用户授权后验证个人资料库；无网页窗口 |
| 3 | Lite 下载适配与自有播放 | 合成媒体、完整回执、失败重试、取消和资源释放；真实媒体验收须另外授权 |
| 4 | 目录/任务/设置 UI 接入，移除旧网页与 WSL 入口 | Playwright + Electron fixture；安装包运行依赖审计 |
| 5 | 2.0.0 版本、输入锁、安装/升级验证 | 在线/离线安装包内部审计及无 WSL 目标机验收 |

阶段 1、2、3 为可行性闸门；不能只凭 mock 测试宣布端到端已实现。阶段 0 的细化计划见 `docs/superpowers/plans/2026-10-21-v2-native-readiness.md`。其余子系统在对应闸门验证出实际接口后分别制定实现计划，避免编造尚未验证的登录/播放接口。

## 不变的边界

- 不自动读取真实配置、账号、Cookie 或 token，不在本轮登录或下载媒体。
- 2.0 使用独立状态目录；1.x WSL、任务、下载、凭据不自动迁移、停止、删除或覆盖。跨版本迁移另行显式设计。
- 不修改用户 DNS、路由、代理、默认发行版、`.wslconfig`，不运行 terminate/unregister/shutdown。
- 无内置网页不等于无账号依赖；没有完成对应授权和能力验证时必须明确不可用原因。
- Wrapper 源码许可不等于其 Android/Apple 运行时材料许可；现有 `publicReleaseAllowed=false` 不随版本升级自动解除。
- 不提交、不推送、不公开发布。用户随后授权了私有测试打包；版本号与实际 2.0 运行入口一起更新，不以仅改版本号代替迁移。

## 工作树基线记录（2026-10-21）

- 已在新工作树执行 `npm ci --ignore-scripts --no-audit --no-fund`，退出码 0。未安装或启动 DRM 浏览器运行时。
- 未修改应用源码时执行 `npm test`：404 项，398 通过、6 失败，退出码 1。
- 为分离沙箱权限影响，在普通宿主权限下重新运行除 `test/electron/drm-builder-integration.test.js` 之外的测试：395 项，394 通过、1 失败。该次排除的 9 项 builder 测试在前一次完整运行中已通过；这不是一次完整全绿回归。
- 五项在宿主权限下消失的失败来自沙箱临时目录的 rename/原子发布操作。剩余失败是 `test/electron/wsl-manager.test.js` 加载时找不到新检出未生成的 `electron/bin/amdl-windows-probe.exe`（ENOENT），并非已证明的 2.0 功能缺陷。
- 随后执行 `npm run build:windows-helper` 生成忽略的测试准备产物，并在宿主权限下完整重跑 `npm test`：587 通过，0 失败。没有删除测试或改动 1.4 运行代码来绕过基线。

## 第一批实现与证据（2026-10-21）

- `electron/native-capabilities.js`：独立能力合同，6 项红/绿测试；缓存地区不能当登录证据，下载成功不能推出完整播放可用。尚未接入桌面状态灯。
- `internal/wrapperlite`：仅本机端口的 Lite 状态客户端；响应合同、大小上限、重定向拒绝、预取消/请求中取消、固定错误及不带凭据均有 fixture。
- `internal/nativeruntime`：资源 manifest 的固定 commit/逐文件 SHA-256 检查、独立且互斥的状态镜像、TCG 服务参数、启动等待、取消、重复启停和退出状态撤销。
- Windows QEMU 进程通过 Unicode API 暂停创建，先加入 kill-on-close Job Object 再恢复。首批通过的是含中文、空格和逗号工作目录的真实合成子进程；真实 QEMU 的后续验收见下节。
- `cmd/amdl-desktop`：独立 `2.0.0-dev` 原生控制 EXE；JSON-lines 的 check/status/start/stop/shutdown 回执、EOF 清理、启动中取消、拒绝未知输入和回执写入失败的非零退出均有测试。
- `npm test`：595 通过，0 失败；`npm run test:native-backend`：3 通过，0 失败。后者执行真实原生 EXE、清空 PATH、使用非可执行合成 bundle 检查完整回执与输入摘要，不启动 guest。
- 新增三个 Go 包的 `go test -mod=readonly -race ./internal/wrapperlite ./internal/nativeruntime ./cmd/amdl-desktop -count=1` 通过；`go vet ./...` 通过。
- 尝试完整 Windows Go race 回归时，旧根包 Wrapper 会话测试在目录 `Sync()` 报 Access denied。已在未修改的 1.4 工作树单独复现 `TestWrapperSessionReceiptBindsCredentialsWithoutPersistingThem` 同一失败；没有修改旧会话实现。不能将完整 Go race 宣称为通过。
- 首批时 Docker Linux 引擎未运行，无法补验 Linux 全量 race；后续用户启动引擎后已补验，见下节。
- 首批时尚无锁定、构建和验证的 Lite/QEMU bundle；该事实由下节的新实测替代。整个过程中没有登录、调用媒体许可或下载媒体，也没有把局部运行原型称为完整 2.0。
- `package.json` 仅增加原生后端构建/测试脚本；现有桌面入口及版本仍为 1.4.3，未冒充可发布的 2.0。下一阶段完成真实 bundle 验证，再将自有 UI 接入，不连接到缺失任务 API 的控制后端。

开发入口和协议见 `docs/development/v2-native-runtime.md`。

## 真实 guest 构建与验收（2026-10-21）

- 在同一个 v2 工作树新增独立 `native-runtime/inputs.lock.json`、严格 TLS 补丁、Docker 构建配方和最小 init。固定 Wrapper commit、源码 SHA-256、QEMU 发布者 SHA-512、toolchain image ID、NDK/CMake/cJSON/Dobby、Debian snapshot 与包版本；toolchain 实际为 Debian 12，旧镜像标签不代表 guest 发行版。
- Windows QEMU 安装程序仅作归档解包，未执行安装、修改虚拟化功能或调用 WSL。Docker 只完成开发构建/export；原生控制 EXE 直接拥有 QEMU 进程，以 `-accel tcg` 启动。
- 真实启动发现并修复：QEMU 文件参数对绝对中文路径处理失败；最小 guest 缺少 ext4 动态使用的 CRC32C 算法模块；缓存规则误拒绝 QEMU 原有带逗号固件文件。前两项均有自己的空镜像串口证据，中文 CWD 和逗号固件有确定性红/绿回归。
- 原生后端将已验证内核/initramfs/固件复制至独立状态目录的内容寻址缓存，只传 ASCII 相对参数；缓存变更拒绝运行，升级不覆盖已有镜像或旧输入。`native-runtime/init` 强制 LF，避免 Windows 检出转换使 guest 无法启动。
- 去除上游源码内硬编码的 TLS 绕过及调试切换，CurlEasy 固定 peer=1、host=2、`/usr/local/android/cert.pem`。空账号不抓取或刷新 token。该补丁已针对固定源码检查、应用并完成 Release 编译；尚未做 Apple 实时 TLS 或登录验收。
- 资源包 `dist-v2-native-runtime-20261021-second` 与 `...-third` 的 guest assembly 确实分别执行（不同 `GUEST_REPRO_RUN`，不是该步骤命中缓存）；190 个文件逐字节一致，manifest SHA-256 为 `f732e1fa9b2b8b37ffe84a9a80090f6711482887d9745cdbe905671d17537f4c`。这是固定编译输出的两次 guest 组装/export 验证，不是跨机器完整 toolchain 重建可复现的承诺。比对证据：`.tmp-v2-native/repro-b960cfc4-c92e-4af7-873c-0fd3cdad6b2d.json`。
- 最新实际 Windows 控制验收：两次 TCG 启动分别 29461 ms、29431 ms；Lite `/status` 返回有效空地区集合；`sessionVerified=false`。端口占用拒绝、完整回执、停止释放端口、启动中取消、EOF 清理和模板不变均通过。证据：`.tmp-v2-native/live-04ac0be6-83e1-406b-aeec-bcd3abb55694/evidence.json`。guest `restrict=on`，未发起登录或媒体请求。
- 新增包 Windows race 通过；完整 Linux `go test -mod=readonly -race ./... -count=1` 与 `go vet ./...` 在源目录只读、网络禁用的容器中通过。最终完整 Node 为 600 通过、0 失败（含新增比对器反例）；原生 EXE smoke 为 3 通过、0 失败。原有完整 Windows race 的目录 Sync 限制仍需区别对待，未宣称其已修复。
- 当前 stop 通过 Job Object 回收进程；真实账号启用前仍须实现并验证 guest 优雅关机、写入持久化与崩溃恢复。此处验证的是同一空镜像复用，不是 Apple 会话或重启免验证码已经验证。
- 本机运行链未调用 WSL、Docker 或内置网页，但尚未在未安装 WSL 的干净 Windows/Win10 目标机验收；未重启本机做持久化测试。现有 `npm start` 仍为 1.4.3 入口，未接入不完整任务 API。

## 原生登录、音频下载、播放与 UI 集成（2026-10-21）

- 默认开发命令 `npm start` 已选用 `electron/start-native.js` → `src/main/native-main.js`；本地 UI 通过固定 IPC 调用 `amdl-service.exe __native-desktop`。该入口不导入 WSL、远端 Apple 网页、隐藏播放器、Cookie 或 DRM 更新模块；Electron 仅承载本地页面，不是 WinUI 重写。
- 单一登录窗口：先恢复并验证 guest 自己的缓存；无会话才自动打开，2FA 在同一窗口完成。网络或运行时故障显示重试，不强迫重新输密码；成功关闭窗口，主动关闭后不会被轮询反复打开，设置复用同一窗口。
- guest agent 使用 per-boot capability；Lite 的 native token/decrypt 接口仅在 guest loopback 提供。账号和验证码走固定子进程 stdin，失败子进程不能凭旧 token 文件发布成功。恢复验证调用 `/v1/me/storefront`，401/403 与网络/schema 失败分开。缓存地区不是登录证明，登录成功也不证明所有个人资料库权限。
- 下载复用现有歌单分页、Catalog ID、封面、歌词、标签、文件发布和完整回执链路，仅在 native 子进程模式替换 manifest/sample 解密传输。guest native CBCS 操作保留原上下文，不把 `/key` opaque 内容猜作 AES key，也不要求 runv3 外部 CDM 文件。当前范围为 AAC-LC、ALAC、Atmos 音频；MV、station 明确未迁移。
- 重启恢复的待执行任务等待 guest/会话，不因启动时序直接失败；仍可取消。凭据只经既有受控 pipe 传给隔离子进程，Job/SQLite/argv/env 不存账号、token 或 guest capability。
- 单个“播放”先准备完整 AAC 媒体，再由本机服务提供授权 Range 音频给本地 `<audio>`；不是即时远端流播放。取消/切歌用 generation 撤销旧结果，关闭文件并删除自己创建的缓存；失败仅回退到真实 preview 并明确标记试听。合成 MP4 只验证容器，UI 用合成 PCM WAV 单独验证可解码音频元素。
- 下载状态灯同时要求新鲜会话、队列就绪、executor 和当前音质可提交，不保证下一首许可或最终成功。目录、私有歌单、任务、设置、文件名工具和日志导出接到 native API，无 developer/MUT 手填框。
- 独立 `AppleMusicDownloader-v2-dev` 状态目录；fixture 用带标记的新 profile。未导入真实 1.x 配置/凭据，未修改或终止任何 WSL 发行版。

### 最新证据与范围

- `dist-v2-native-runtime-20261021-session-b` 与 `...-session-c` 两次强制 guest assembly/export，190 文件字节一致；manifest SHA-256 `c5d6067c1d7df97742cc19db3674affee495dff8897a91cbfe2acef1cbf9b9d7`；`.tmp-v2-native/repro-40b98076-098d-4646-8c2e-8602d4e23ded.json`。不承诺跨机器完整工具链重建相同。
- protocol-1 guest 两轮、每轮两次真实 TCG 启停通过；最新 14952/11231 ms。验证 needs_login、端口占用、优雅关机（QEMU 正常退出，不仅 Job Object kill）、启动中取消、EOF 清理、模板不变；`.tmp-v2-native/live-ccb13dd7-7bf4-468b-b0ef-032c2aa94b17/evidence.json`。早期一次退出挂起未确认根因；加入阶段输出后两轮未再复现，不宣称已定位修复。
- 最终真实原生服务 EXE 验证未授权 API/未登录入队拒绝、默认并行数 3、离线包不尝试登录、任务库无 sentinel、EOF 后 guest/端口释放；`.tmp-v2-native/desktop-8598a05e-09c6-4800-903c-c5bbd2c6e3e9/evidence.json`。
- 最终实际 Electron 借用已有壳二进制，使用新 profile/isolated guest；验证无会话自动唯一登录窗口、设置复用、关闭不反复打开、无 iframe/webview；`.tmp-v2-native/electron-1cbbab64-79ab-4935-9134-d683e1d6b31e/evidence.json`。未读旧 profile、未运行网页 DRM；不是标准 Electron 新依赖/安装包已迁移证明。
- 最终 Node 全量 605 项、Playwright 74 项、Linux 全量 Go race/vet 通过，Windows native 定向 race 通过。新增原生设置/凭据边界/恢复任务/延迟音频错误回退均有回归；旧根包 Wrapper 目录 Sync 限制不作已修复宣称。已检查 tracked diff 及未跟踪源文件的空白问题；未 stage/commit/push。
- 另外构建 `dist-v2-native-runtime-20261021-network-dev`（`isolated=false`），仅供后续手动交互验收，未运行或登录。与 session-b/c 具有相同 guest 编译内容，网络 manifest 是单独身份；不以它通过离线 fixture，也不是安装包。

此集成里程碑当时尚未验收真实账号恢复/2FA/重启免验证码、个人资料库权限、真实媒体解密和完整播放、MV/station、无 WSL 干净 Win10/11 机器、标准 Electron 依赖与安装包资源迁移；当时发布元数据仍为 1.4.3、EXE 为 `2.0.0-dev`。后续安装包迁移见下一节，真实账号/媒体及干净目标机限制未解除。

## 私有测试安装包（2026-10-22）

- 当前 package/lock、选用的服务 EXE、UI 和安装器版本统一为 `2.0.0-beta.1`；标准 Electron 精确为 `45.0.0`。历史 1.4 元数据冻结到 `electron/legacy-desktop-package.json` 供旧回归使用，不进入新应用归档。
- 默认 `dist:win`/online/offline 均转到 `dist:native`，生成一个自带运行时的私有 NSIS，不再导入 WSL 打包配置。归档只含 native main/preload/renderer 和固定业务模块；资源为 Windows 后端、QEMU/guest、输入证据与许可声明，无旧 WSL helper/远端网页模块。
- app ID 为 `io.github.andrew05213.applemusicdownloader.v2test`，产品为 `Apple Music Downloader 2.0 Test`；独立 `%APPDATA%\AppleMusicDownloader-v2-test`，默认下载到用户 Music 下的 `Apple Music Downloads 2.0 Test`。不覆盖/导入 1.4 数据；无 WSL 自定义安装卸载动作，卸载保留数据。
- 网络运行时 manifest SHA-256：`b1f4b9c5003d053c9342d759440c4eac81fdd97773f405fb7e77df74bd877406`。测试包包含此网络资源；自动 fixture 显式改用独立的 isolated 空账号包，不能把离线 fixture 当成 Apple 网络验证。
- 成功产物：`dist-native-2.0.0-beta.1-60a3838e-e48e-4659-9853-024e4664473f/AppleMusicDownloader-2.0.0-beta.1-Native-Test-Setup.exe`，244156611 字节；SHA-256 `b07d9f87edc28a04b28968ceae64d0fc14cfec83031433dff0b232b05948f2aa`。未签名，未自动安装。
- 审计：仓库 `7za.exe` 的 `l/t/x` 成功并直接解出内嵌 7z 应用，276 个文件与最终 `win-unpacked` 字节一致；核对 asar 入口/版本/UI、198 个受收据约束的资源、后端和 runtime 摘要。完整系统 7-Zip 另只读确认外壳为 `NSIS-3 Unicode`。证据：`.tmp-v2-native/nsis-audit-df091627-9876-4a83-b8ce-52ad8ae35a93/evidence.json`。
- 从上述最终安装包解出的 EXE 实际启动，确认 `app.isPackaged=true`、版本 Beta、Electron 45、`app.asar` 入口、无网页视图、唯一自动登录窗口、设置复用和关闭后不再弹出；使用新的 fixture profile，未登录。证据：`.tmp-v2-native/electron-38144687-c628-4821-8dbc-83691f205fa4/evidence.json`。
- 此轮 Node 完整回归最终 611 项通过；Playwright 74 项、Linux 全量 Go race/vet、Windows native 定向 race/vet 在源码稳定后通过。旧根包 Windows 目录 Sync 限制仍未宣称修复。公开发布门禁仍关闭；无账号、无媒体许可/下载请求，无 WSL 或系统设置操作。
- 修复的打包工具问题：Windows ASAR 路径按宿主分隔符提取；构建工具只解压所需 Windows 组件，避开不需要的 macOS 符号链接，不打开系统权限。审计兼容直接提取和内层 archive 两种布局，均有红/绿回归。失败构建目录保留，不冒充最终产物。

用户验收步骤与限制见 [测试指南](../testing/v2.0.0-beta.1.md)。真实 Apple 登录/2FA/会话恢复、账号私有数据权限、音频下载与完整播放仍未由自动测试证明；MV/station 明确未迁移。`publicReleaseAllowed=false`，没有提交、推送或公开上传。

### 验证码说明重打包（2026-10-22）

最新私有包仍为 `2.0.0-beta.1`，新目录 `dist-native-2.0.0-beta.1-1410152f-d8fa-4ff0-b40d-1d516375695a`，安装器 SHA-256 `645dedea7ad8b47c1a68a9c8abd678db1941ddb3513aacfce9851e95347e857b`。包含默认收起的受信任设备/短信/语音获取说明和重复状态轮询不抢焦点的修正，不包含短信发送接口。

随后经批准新增了实验性 `internal/grandslam` SRP/SMS 协议模块和离线可测试的 `native_adi` 适配层；当时尚未接入 guest、IPC 或登录界面。此处描述的是旧安装包，后续源码进展见下节。

后端/guest 没变；源 UI 摘要、新包 276 文件、198 资源及实际解包 EXE 的隔离启动均通过校验。审计 `.tmp-v2-native/nsis-audit-150af7b5-4cd6-4a3b-ae3b-79f6709e3372/evidence.json`，启动 `.tmp-v2-native/electron-cc55dfbd-3d8d-4a8e-9a38-9e2790cb2b42/evidence.json`。重跑 Node 611、Playwright 77、原生 EXE smoke 3 项均通过；旧包哈希不变。真实账号/媒体与安装卸载限制维持上述说明。

## 原生短信到 Lite 会话接线（2026-10-22，源码阶段）

- 源码已连接本地 ADI helper/provider、SRP、受信任手机号选择、短信发送/验证、一次重新 SRP 和 PET-as-password 的 Lite 签名认证。旧设备验证码仍是默认方式；没有新增网页、远端 Anisette 服务或手填 token。
- 单一登录窗口新增“短信验证码（测试）”、掩码手机号、发送回执、重发冷却、错误和取消。短信接受只进入 `binding`，仍需 Lite SDK 返回实际商店凭据，再取得新鲜 Music token 并验证 storefront，才能成为 `authenticated`。
- PET 不是 XToken/MUT。该交换有固定上游代码参考，但当前 Android/GSA 身份和 Music 服务的真实接受情况尚未经账号验收，不能把协议接线或 mock 全绿称为真实短信登录成功。
- 已编译完整新 guest、Windows 控制器和后端，并验证实际 Bionic helper 的无网络 SDK 初始化/未 provision 状态。空账号 QEMU 两轮启停、优雅关闭、取消及 EOF 清理通过；未触发真实 provisioning、短信、登录或媒体。
- 此源码阶段没有生成安装器，旧 `...-1410152f-d8fa-4ff0-b40d-1d516375695a` 包不含此功能；后续私有打包见下节。详细模块、证据及用户验收条件见 [原生短信实现状态](../development/v2-native-sms-login.md) 和 [接线实施记录](../superpowers/plans/2026-10-22-v2-sms-music-binding.md)。

## 原生短信私有测试包（2026-10-22）

- 最新包仍为 `2.0.0-beta.1`，独立目录 `dist-native-2.0.0-beta.1-08028701-5cec-45e0-8f18-517c92da25e4`；安装器 244329107 字节，SHA-256 `18c2e0a0ce0f728b53f038d56c538b2fcf6ce9a3e23d904ea4ee94282ec0722b`。旧包均保留；未签名、未安装、未发布。
- 新联网运行时 `dist-v2-native-runtime-20261022-sms-network`，manifest SHA-256 `ffa37f60ae18140f0aa71722e4e3684eb0875461fa9135fd06a7648c386399fd`。全部 runtime 文件与最终隔离 `sms-b` 一致，仅 manifest 的网络策略不同。guest agent SHA-256 `d7a1b4f32305f821f151ccbc3cef5715b69cfd3266642888e510c446498ef6eb`，完整 Store/ADI/Go 输入新鲜度核验通过。
- 仓库 `7za.exe` 的 `l/t/x` 和最终解包摘要审计通过：276 文件一致，198 个受收据约束的资源；后端 SHA-256 `0a7183641a5226f98927d71e36bffefd6f4134f918b51e8d67dec91910501246`。证据 `.tmp-v2-native/nsis-audit-77cc278c-112d-4e06-94ab-ac2177f5b1fc/evidence.json`。
- 最终安装包解出的 EXE 实际启动，验证 packaged/Electron45/版本、唯一自动登录窗口、短信选项可选择、无远端网页、设置复用与关闭不重开；使用新 profile 和隔离 guest，不输入账号。证据 `.tmp-v2-native/electron-76a47cd7-31d3-408b-bf8c-904aead0a687/evidence.json`。
- 本轮 Node 621、Playwright 80，以及短信相关六包 Windows/Linux race/vet 通过；未将其宣称为新的全量后端回归。仍需用户真实短信/Music 会话验收，未调用 Apple 登录、发送短信或媒体；未提交或推送。操作见 [测试指南](../testing/v2.0.0-beta.1.md)。

## 阶段日志与错误保留测试包（2026-10-23）

- 修复上一包错误码被过滤、取消/关窗后无法追溯失败的问题。guest 保存固定阶段历史和最后失败，桌面原子持久化最多 256 条诊断；导出 schemaVersion 2，不记录原始输出、账号、密码、短信码、手机号、响应体或 token。
- 最新独立目录 `dist-native-2.0.0-beta.1-b6e6d6df-16e7-4a35-a647-a59fbe375706`，安装器 244343887 字节，SHA-256 `9b541e378ecaa7991af995ae91cb203e82c0691ca8285d3422e7145996ad169a`；版本仍为 `2.0.0-beta.1`。旧包哈希不变，未安装或公开发布。
- 新联网 guest manifest `132b7dde61fb46320a633b5d7b5b5deaeb7b5d9c2deebace928be654b3ea12cc`，guest agent `af00c1d82ae8267db24321aa5e0c26d44912ad4d9d4beba4b34d345435737ea0`；运行时文件与新隔离 guest 相同。后端 `f34aa48a5827ee8602b962a728febd1421e85755da09445b3b19879b7f7e185e`。
- 仓库 7za `l/t/x`、198 资源收据和最终解出 276 文件校验通过；审计 `.tmp-v2-native/nsis-audit-c2b08a7c-a5fc-42d1-9d8e-13fd0d7a5c95/evidence.json`。
- 最终解包 EXE 使用新隔离 profile，通过短信入口、合成历史失败展示、关窗不重开、实际按钮导出和持久化恢复检查；`.tmp-v2-native/electron-0732d78f-3471-4965-972b-0bff1a045bc2/evidence.json`。合成失败不是真实 Apple 失败捕获。
- Node 627、Playwright 81、原生 EXE smoke 3、Windows 原生根包定向 race 及六个短信包 Windows/Linux race/vet 通过。真实 guest 空账号两轮启动 12087/11235 ms、优雅关闭/取消/EOF 检查通过；`.tmp-v2-native/live-c3861a33-ee2e-493e-8550-164da57eb6c4/evidence.json`。未宣称新的全量后端回归或实账号登录成功。

## 已查阅的上游资料

- [Wrapper Lite 固定源码](https://github.com/WorldObservationLog/wrapper/tree/2fd7e0eaef081f93cf687428b965674937832ba4)
- [Lite HTTP 实现](https://github.com/WorldObservationLog/wrapper/blob/2fd7e0eaef081f93cf687428b965674937832ba4/lite/lite_main.cpp)
- [QEMU 运行说明](https://github.com/WorldObservationLog/wrapper/blob/2fd7e0eaef081f93cf687428b965674937832ba4/qemu/README.md)

以上为设计输入，不是本项目 Windows/QEMU 的实测证明。
