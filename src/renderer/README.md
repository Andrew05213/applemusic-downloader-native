# Renderer 边界

本目录包含 Native 桌面的 `native-*` 资源和 `native.css`，由原生安装器白名单收录。界面通过受控 preload/IPC 与本地服务交互，不加载官方 Apple 网页。

共享 Go/Web 界面位于 [web/](../../web/README.md)，不随桌面版分发；旧桌面和 WSL/DRM 参考代码位于 `legacy/v1/`。

播放器链路见[分片流式播放](../../docs/architecture/streaming-playback.md)，后端职责见[依赖边界](../../docs/development/dependency-boundaries.md)。
