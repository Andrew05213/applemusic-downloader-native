# 依赖边界

## 原生运行链路

| 入口 | 职责与依赖 | 不依赖的旧内容 |
| --- | --- | --- |
| Electron main、preload、renderer | 原生窗口、受控 IPC、自有界面与播放器；安装器使用白名单 | 共享 Web、旧桌面网页壳 |
| Go 根包 → `amdl-service.exe` | 队列、目录、文件与标签、原生登录、下载和分片播放 | 归档的 Appliance/WSL 专属包 |
| `cmd/amdl-desktop` | 诊断控制后端；依赖 nativeruntime、guestclient、nativeauth、wrapperlite | 旧 WSL 探针与引导程序 |
| `cmd/amdl-guest` | guest agent、认证和原生会话协议 | 旧 Appliance supervisor |
| `native-runtime/` | 锁定 Lite、QEMU、工具链镜像与 guest 源码，构建运行组件 | 归档 Wrapper 源码和旧 rootfs |

Go 路径相对 `apple-music-downloader/`。根包仍为 `package main`，会编译部分 CLI/Web、`wrapper_manager.go` 和 `utils/runv3` 兼容代码，不能宣称旧实现已全部从服务 EXE 中移除。

桌面下载使用 `native_download.go` → `runv2` → guest decryptor；播放使用 `native_playback.go` → `runv2.OpenStream`。二者不复用旧 Web/Wrapper 的控制生命周期，详见[流式播放](../architecture/streaming-playback.md)。

## 共享与归档

`web/` 为 Go serve 的兼容界面，不进入桌面安装包。`serve` 从 Go 模块目录默认读取 `../web`，从根目录也可发现 `web/`；显式 `--static-dir` 仍有效，不自动暴露原生 renderer。

`legacy/v1/` 的专属 Go 包已离开当前 Go 模块的测试和编译发现范围。部分兼容守卫测试仍读取归档锁文件，这是来源回归，不代表 runtime 从旧目录构建。

`appliance/release-policy.json` 仍被打包和校验使用，保持关闭；原生策略单独管理。工具链镜像也有独立的锁定身份，归档配方不等于已提供其可复现构建方法。

## 修改时的约束

- 不使用 build tags 隐藏共享安全回归。
- 不将旧 Wrapper 启动、账号目录或进程生命周期接入原生 guest 登录。
- 移动文件前检查构建白名单、哈希与固定路径，保留原始许可证和来源清单。
- 后端拆分按窄接口迁移，不直接移动整个 main/server 或删除共享媒体函数。
- 安装标识与数据路径遵守[兼容说明](compatibility.md)。

接口细节见[共享 Go 接口](shared-go-interfaces.md)和[JobAPI 文件边界](job-api-file-boundaries.md)。`npm run verify:boundaries` 对 Windows service/control 和 Linux guest 运行只读 `go list -deps`，防止重新引入归档包；完整检查见[测试指南](../testing/README.md)。
