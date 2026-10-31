# 2.0 原生开发入口与验证

> 历史记录：以下内容保留当时的实现、命令与验收语境，不代表当前发布状态。当前指南见[文档导航](../README.md)，不要直接用本页旧命令安装或发布 2.0。

当前为 `2.0.0` 原生安装版；维护者已打开原生公开构建开关，第三方源码与授权核查仍未完成，见[核查清单](../compliance/native-2.0-status.md)。版本/安装包和隔离启动验证见 [2.0.0 本地包记录](../testing/2026-10-25-v2.0.0-local-package.md)。为保留 Beta 的账号和任务，沿用既有 app ID、NSIS GUID、资料与默认媒体目录的兼容名称。默认 `npm start` 进入自有 UI，原生后端直接管理 QEMU TCG guest；运行不调用 WSL/Docker。Docker 仅用于开发构建。标准 Electron 已锁定为 `45.0.0`；1.4 元数据只冻结供历史回归使用，不进入新应用归档。下文 Beta 构建示例保留为历史用法。

## 构建

需要 Windows Go 1.25.0、锁定的 Linux toolchain 镜像和已就绪的 Docker Linux 引擎；不读取 1.x 配置或账号。

```powershell
npm run build:native-backend
npm run build:native-service
npm run build:native-runtime -- --out dist-v2-native-runtime-run-a
npm run build:native-runtime -- --out dist-v2-native-runtime-run-b
node test/native/native-guest-repro.cjs dist-v2-native-runtime-run-a dist-v2-native-runtime-run-b
```

输出目录必须不存在。每次强制 guest assembly/export，不能以该步骤命中 cache 冒充复现证据；每个资源和所有输入/补丁/agent 源码摘要均校验。assembly UUID 可不同，其余固定输入必须一致。锁在 `native-runtime/inputs.lock.json`、`guest-agent.lock.json`。

默认 **isolated** 包禁止 guest 出站，原生服务也阻止在线目录和登录，仅供无账号测试。需要用户交互验收时另建新网络包（命令只构建，不登录）：

```powershell
npm run build:native-runtime -- --out dist-v2-native-runtime-network-dev --allow-network
```

两类包 manifest 身份不同，不能手改已有 bundle，不能给 fixture 指向真实账号镜像。真实账号或媒体没有在本轮使用。

## 启动本地界面

先安装锁定的 Node 依赖和标准 Electron EXE；不启动网页、隐藏播放器或 Widevine 更新。也可显式指定相同版本的标准壳 EXE，只读二进制，不读其旧 profile：

```powershell
$env:AMDL_NATIVE_ELECTRON_EXE = 'E:\electron-runtime\electron.exe'
npm start -- --native-manifest 'E:\AMDL-v2-runtime\bundle.json'
```

不设置变量时使用本工作树 `require('electron')` 的 EXE。资源必须 `controlProtocol=1`，缺少二进制报 `native_electron_unavailable`。不会自动安装 WSL、修改可选功能、下载 DRM 内核或寻找旧账号配置。

数据使用独立的 `%APPDATA%\AppleMusicDownloader-v2-test/native-v2`；默认输出为用户 Music 下的 `Apple Music Downloads 2.0 Test`。先恢复并验证 guest 缓存，无会话才打开唯一登录窗口；2FA 在同一窗口，成功关闭，主动关闭不反复弹出。网络/运行时故障先重试，不自动提交密码。

账号/验证码经固定动作和 guest stdin；token/guest capability 仅通过内存与受控子进程 pipe，Job/SQLite/UI 不保存凭据。缓存文件或地区不是登录证明；验证 `/v1/me/storefront` 后才能用于个人操作。

## 组件

- `amdl-desktop.exe`：诊断控制原型，stdin JSON-lines check/status/start/stop/shutdown 和 protocol-1 会话动作；不是最终 UI 后端。
- `amdl-service.exe __native-desktop`：自有 UI 后端，显式 manifest/data/output 绝对路径、guest 端口；stdin 首行接收私有 API capability，EOF 取消任务并关机。
- guest `amdl-guest`：从 TMPFS 读取并删除 boot capability，拥有固定 Lite login/service；Lite 仅绑定 guest loopback，宿主只转发 agent 端口。

QEMU 固定 TCG、512 MiB、2 vCPU；中文路径通过 Unicode CWD、内容寻址输入缓存和 ASCII 相对文件参数处理。独立镜像文件锁防止多个实例共享写入；异常不删除用户状态。

退出先通知 agent，取消登录/媒体、等待 Lite、关闭 HTTP、PID1 sync/unmount/poweroff；超时才回收自己 Job Object 内的 QEMU，不操作其他应用或发行版。

## 下载与播放范围

原生适配 AAC-LC/ALAC/Atmos manifest 与 CBCS sample，复用原元数据、歌词、封面、文件发布和完整回执，不要求 runv3 外部 CDM。MV/station 未迁移；个人歌单需要相应 API 权限，不能仅凭登录成功推断全部资源可用。

完整播放先准备 AAC，再用授权本机 Range 音频供 `<audio>`；不是即时流播放。一个播放按钮，失败才回退有标记的试听。重启恢复的待执行任务等待会话而不因启动时序直接失败，等待可取消。绿灯只代表当前音质可提交且会话/队列就绪，不保证媒体最终成功。

## 无账号测试

```powershell
npm test
npm run test:web
npm run test:native-backend
npm run test:native-guest -- --bundle dist-v2-native-runtime-run-a
node test/native/native-desktop-live.cjs 'E:\AMDL-v2-runtime\bundle.json'
node test/native/native-electron-live.cjs 'E:\electron-runtime\electron.exe' 'E:\AMDL-v2-runtime\bundle.json'
npm run verify:release-gates
```

live fixture 必须 isolated 包，每次创建 `.tmp-v2-native/<类型>-UUID`，不接受真实 profile/账号镜像，不发起登录/媒体许可。Electron fixture 标记专用 profile；真实客户端默认不启用此参数。

Go 新包可在 Windows 定向 race 验证；Linux 全量 race/vet 使用源码只读、`--network none` 容器。原有 Windows 根包 Wrapper 目录 `Sync()` Access denied 已在未改 1.4 重现，不宣称全量 Windows 通过。

合成 MP4 仅证明容器/回执/Range；合成 WAV 仅证明音频元素可解码，不能代替真实 Apple 媒体解密验收。本机证据和历史挂起见架构文档最新集成段。

## 私有测试打包

```powershell
# 先按上文生成 --allow-network bundle；这里只选择它，不登录。
$env:AMDL_NATIVE_BUNDLE = 'E:\AMDL-v2-runtime-network'
npm run dist:native
node electron/audit-native-installer.js '<新输出目录>\AppleMusicDownloader-2.0.0-beta.1-Native-Test-Setup.exe'
node test/native/native-electron-live.cjs '<新审计目录>\outer\Apple Music Downloader 2.0 Test.exe' '<isolated bundle>\bundle.json' --packaged
```

每次打包使用新的 GUID staging/output，不覆盖旧包；`--publish never`。`AMDL_NATIVE_BUNDLE` 必须显式选择 bundle **目录**，不是 `bundle.json` 文件；没有旧包默认值。先校验网络 bundle/空模板、Store/ADI/完整 guest Go 源码的新鲜度和原生 EXE/UI，再生成资源收据；afterPack 和解包审计重新校验摘要。源码改变后必须重建 runtime，不能将新短信 UI 与旧 guest 混包。

Windows 构建所需 `winCodeSign-2.6.0` 归档固定 SHA-512，只准备 Windows 组件，不启用符号链接权限或其他系统功能。仓库精简 `7za` 可能直接解出 NSIS 内嵌 7z；审计支持该布局及带内层 archive 的布局，保留 `l/t/x` 输出并核对最终文件。

成功包与实测见架构文档末尾；手动验收见 [Beta 测试指南](../testing/v2.0.0-beta.1.md)。未签名，不自动执行安装器，不自动导入任何旧账号。

## 剩余门槛

真实登录/2FA/系统重启会话、真实媒体、实际安装/卸载、无 WSL 干净目标机和再分发许可仍需验收。原生构建开关已打开，但历史私有测试记录中的门禁状态只代表当时状态。旧代码保留用于回归，不在新的默认开发入口/包内加载。
