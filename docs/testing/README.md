# 测试指南

所有命令从仓库根目录运行。环境准备见[开发指南](../development/README.md)。

## 单元与结构检查

```powershell
npm run verify:native
npm run verify:boundaries
npm test
node scripts/check-docs.js --all
git diff --check
```

`npm test` 使用 `electron/native-ci.js` 的原生/共享集合，包含文档回归。`npm run test:all` 扫描当前 `test/**/*.test.js`，`npm run test:electron` 扫描 Electron 单元测试。它们不包含 `legacy/v1/` 的旧专属测试。

Go 模块位于 `apple-music-downloader/`。Linux CI 执行全量 race/vet 和 guest 交叉编译。Windows CI 编译 backend/service，运行协议 smoke 和 Go race；已知旧 Wrapper 会话 directory-Sync 用例在 Windows 跳过，由 Linux 全量覆盖。当前矩阵和跳过表达式以 `.github/workflows/verify.yml`、`electron/native-ci-windows.ps1` 为准。

Windows Go race 需使用 ASCII 临时路径。CI 适配仅设置测试进程的临时路径和默认所有者，并恢复状态，不放宽产品 ACL、目录身份或重解析点校验。

## UI 回归

```powershell
npx playwright install chromium
npm run test:web
```

普通 Chromium 回归不等于 AAC 解码验证。真实 AAC 测试需设置 `AMDL_STREAM_OUTPUT` 并使用支持 AAC 的浏览器；`AMDL_STREAM_BROWSER` 可指定已安装 channel。缺少夹具时会跳过相关用例，报告中必须注明。

## 合成 AAC 完整链路

需要开发者安装的 FFmpeg 和支持 AAC 的 Chrome/Edge。夹具只包含生成的正弦波，不使用账号、验证码或商业媒体。以下为 PowerShell 示例，输出到新的测试目录：

```powershell
New-Item -ItemType Directory .artifacts/aac-input, .artifacts/aac-output
ffmpeg -hide_banner -v error -nostdin -f lavfi -i sine=frequency=440:sample_rate=48000 -t 42 -c:a aac -b:a 128k -f hls -hls_time 2 -hls_playlist_type vod -hls_segment_type fmp4 -hls_fmp4_init_filename init.mp4 .artifacts/aac-input/index.m3u8
$env:AMDL_STREAM_FIXTURE = (Resolve-Path '.artifacts/aac-input').Path
$env:AMDL_STREAM_OUTPUT = (Resolve-Path '.artifacts/aac-output').Path
go -C apple-music-downloader test -count=1 -run '^TestStreamGeneratedAACFixture$' ./utils/runv2
ffmpeg -hide_banner -v error -xerror -nostdin -i .artifacts/aac-output/complete.m4a -map 0:a:0 -f null -
$env:AMDL_STREAM_BROWSER = 'chrome'
npm run test:web
node electron/native-build-environment.js test/native/native-stream-live.cjs
```

转换导出生产代码处理后的初始化数据、分片、收据与完整串接流。FFmpeg 完整解码与浏览器直接拖动、延迟尾片测试互相补充；Electron live 测试覆盖真实 sandbox preload、二进制 IPC、能力 HTTP 与 MSE。

CI 的 Linux 示例见 `.github/workflows/verify.yml`，不依赖旧 Docker/WSL 构建镜像。

## 原生动态封面

动态封面回归位于 `internal/motionartwork/` 与 `animated_artwork_test.go`，覆盖 HLS 清晰度选择、相对地址和重定向、显式及隐式字节范围、非零时间戳归零、B 帧时间戳、样本完整性、取消和损坏响应，以及临时文件、覆盖策略和文件回执。应用的下载路径使用 Go HTTP 和现有 MP4 库；测试会清空工具搜索路径，确认无需安装 FFmpeg。

```powershell
go -C apple-music-downloader test -count=1 -run 'TestAnimatedArtwork' .
go -C apple-music-downloader test -race ./internal/motionartwork
```

CI 还用开发工具生成 4 秒、120 帧的合成 H.264/fMP4 HLS 输入，经原生下载器导出后完整解码；这项测试不使用账号或商业视频。`TestDownloadGeneratedArtworkFixture` 通过 `AMDL_ARTWORK_FIXTURE` 指定输入目录（内含 `index.m3u8`），可通过 `AMDL_ARTWORK_OUTPUT` 指定导出文件。未提供夹具时该项跳过，常规回归不受影响。生成和验证命令见 `.github/workflows/verify.yml`。

## 打包应用验收

构建后使用以下工具和参数：

| 工具 | 参数与用途 |
| --- | --- |
| `electron/audit-native-installer.js` | 安装包路径；解包并逐文件对照资源与来源回执 |
| `test/native/native-electron-live.cjs` | 应用 EXE、隔离 runtime manifest、`--packaged`；实际应用启动、登录入口、目录选择、诊断和合成播放 |
| `test/native/native-task-delete-live.cjs` | 隔离 manifest、实际服务 EXE；仅删记录、删除文件、空目录及失败重试 |

live 测试要求已构建的**隔离 runtime**，以及真实 AAC 输出夹具。测试会创建隔离资料，不使用安装后的个人会话。不要将联网发行 runtime 或用户资料直接作为测试夹具；Windows 测试目录应保持常规长度，见[兼容限制](../development/compatibility.md)。

安装器解包与隔离应用测试不等于实际覆盖安装测试。报告记录源码提交、输入与产物哈希、执行命令、跳过和未验证范围，不将测试总数写成永久保证。固定版本结果见[发布记录](../releases/1.0.0.md)。
