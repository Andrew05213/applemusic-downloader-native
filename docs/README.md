# 文档导航

## 使用

- [用户指南](USER-GUIDE.md)：安装、登录、播放、下载、设置与问题反馈。
- [下载与发布记录](https://github.com/Andrew05213/applemusic-downloader-native/releases)：安装包、SHA-256 和各版本验证范围。
- [1.0.0 版本说明](releases/1.0.0.md)：已发布版本的功能与已知限制。

## 开发

- [开发与构建](development/README.md)：环境准备、验证、源码运行与安装包构建。
- [测试指南](testing/README.md)：单元测试、跨平台检查、合成 AAC 和打包应用验收。
- [仓库结构](REPOSITORY.md)：源码入口与兼容代码的职责。
- [依赖边界](development/dependency-boundaries.md)：桌面、Go 服务、guest 与 Web 兼容层。
- [共享 Go 接口](development/shared-go-interfaces.md)：应用接口、退出顺序与宿主适配。
- [JobAPI 与文件服务](development/job-api-file-boundaries.md)：文件身份、句柄所有权和安全删除。
- [分片流式播放](architecture/streaming-playback.md)：媒体链路、缓冲、取消与下载隔离。
- [Catalog API](CATALOG_API.md)：共享目录查询与地区处理。
- [构建存储](development/build-storage.md)：产物、缓存与清理规则。
- [CI 与发布](RELEASE.md)：运行组件输入、安装器验证与正式发布步骤。
- [兼容标识](development/compatibility.md)：模块路径、安装身份和用户资料目录。

## 项目资料

- [上游与依赖来源](UPSTREAMS.md)
- [第三方声明](../NOTICE)与[源码提供、核查状态](compliance/native-status.md)
- [旧源码参考说明](legacy/README.md)
- [文档维护约定](DOCUMENTATION.md)

当前文档描述本仓库 Native 版本。上游 README 和 `legacy/v1/` 中的文件保留原始语境，不是当前桌面版的安装或构建入口。
