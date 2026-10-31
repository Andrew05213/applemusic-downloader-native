# Go/Web 兼容界面

本目录与 Go `serve` 命令配套，不是 Native Electron renderer，也不进入桌面安装包。

从 `apple-music-downloader/` 启动服务时，静态目录默认 `../web`；从仓库根目录启动也可发现 `web/`。`--static-dir` 可指定其他目录，不默认暴露 `src/renderer/`。

Web 的登录和 Wrapper 生命周期属于共享兼容路径，不是原生 Lite guest 的登录或播放适配。仅运行 Web 服务不保证完整媒体能力。回归测试使用合成状态，不使用真实账号。

桌面操作见[用户指南](../docs/USER-GUIDE.md)，开发边界见[依赖说明](../docs/development/dependency-boundaries.md)。
