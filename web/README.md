# 共享 Go/Web 界面

本目录保存与 Go `serve` 命令配套的兼容 Web 界面，不是 2.0 Electron renderer，
不会进入 2.0 安装包。9 个资源从 `src/renderer/` 原样移入，内部相对链接不变。

从 `apple-music-downloader/` 运行 `serve` 时，静态目录默认 `../web`；
从仓库根目录启动的服务也可发现 `web/`。`--static-dir` 仍可显式指定其他目录。
服务不会再自动将 `src/renderer/` 作为 HTTP 静态根目录。

Web 的登录/Wrapper 生命周期仍属于共享 Go 兼容路径，不是原生 Lite guest 登录和播放适配。
整理目录不表示它已变成独立的 2.0 Web 产品，也不保证只运行服务就具备完整媒体能力。
不要使用真实账号来运行普通回归测试。

当前桌面见[用户指南](../docs/USER-GUIDE.md)，开发职责见[依赖边界](../docs/development/dependency-boundaries.md)。
