# Apple Music Downloader Native

[![CI](https://github.com/Andrew05213/applemusic-downloader-native/actions/workflows/verify.yml/badge.svg)](https://github.com/Andrew05213/applemusic-downloader-native/actions/workflows/verify.yml)

用于浏览 Apple Music、播放音乐和管理下载的 Windows 桌面工具。提供原生登录界面、ALAC 下载与分片流式播放，无需安装 WSL、Docker 或使用内置官方网页。

[下载与发布记录](https://github.com/Andrew05213/applemusic-downloader-native/releases) · [使用指南](docs/USER-GUIDE.md) · [开发文档](docs/development/README.md) · [问题反馈](https://github.com/Andrew05213/applemusic-downloader-native/issues) · 交流 QQ 群：1131277036

## 功能

- **浏览与搜索**：首页推荐、排行榜、歌单、专辑和音乐人详情，支持页面缓存与预加载。
- **播放**：AAC 分片流式播放，支持进度拖动；完整播放不可用时回退到试听。
- **下载**：默认 ALAC，支持并行队列、实时速度与进度、失败重试。
- **文件管理**：选择保存位置、点击生成文件名模板、定位文件夹，分别支持仅删除任务或同时删除文件。
- **登录**：原生账号登录与受信任设备验证码验证，保存会话以便后续使用。

播放、下载和目录可用性取决于账号授权、订阅与地区；部分资源可能不可用。

## 下载与安装

在 [Releases](https://github.com/Andrew05213/applemusic-downloader-native/releases) 页面获取已发布的 Windows x64 安装包及 SHA-256 校验文件，版本和附件以该页面为准。

运行安装器并选择安装位置，启动后登录账号。完整操作见[用户指南](docs/USER-GUIDE.md)。

安装包未签名，Windows 可能显示未知发布者提示。请核对下载来源和 SHA-256，不要为安装而关闭系统安全防护。

## 参与开发

开发验证使用 Node.js 22 和 Go 1.25.0。在仓库根目录执行：

```powershell
git clone --depth 1 https://github.com/Andrew05213/applemusic-downloader-native.git
cd applemusic-downloader-native
npm ci
npm test
npm run doctor:native
npm run dev:native
# 构建本地安装包，不上传
npm run package:native
```

运行与打包需 Windows x64 和已就绪的 Docker Linux 引擎；所需工具链会从公开锁定输入构建，不依赖维护者本机镜像。首次完整准备及环境检查见[从 clone 到运行与打包](docs/development/README.md)，跨平台验证见[测试指南](docs/testing/README.md)。

新贡献者可先看[维护与贡献入口](CONTRIBUTING.md)。旧版预编译运行文件已迁至独立 Release 附件，日常 Native 开发无需下载；完整 Git 历史仍包含旧文件，浅克隆可避免下载这些历史二进制。

欢迎通过 Issue 反馈问题、加入交流 QQ 群（1131277036）讨论，或提交 Pull Request。反馈请附版本、复现步骤和已脱敏的诊断；提交改动前运行相关测试，不上传账号、验证码、Token、会话或下载的音乐。

## 项目文档

- [仓库结构](docs/REPOSITORY.md)与[后端接口边界](docs/development/job-api-file-boundaries.md)
- [构建与发布流程](docs/RELEASE.md)
- [依赖与上游来源](docs/UPSTREAMS.md)
- [完整文档导航](docs/README.md)

## 许可证

本项目由维护者拥有版权或有权授权的原创代码及修改采用 [Apache License 2.0](LICENSE)。Copyright 2026 Andrew05213。

第三方导入代码、历史归档、依赖及运行组件不因根目录许可证而改变授权；原有版权和许可证声明继续保留。来源或授权尚未确认的材料也不因此获得再分发许可，范围和说明见 [NOTICE](NOTICE)。

## 第三方说明

本项目与 Apple 无隶属关系。请只访问和保存你有权使用的内容。

第三方组件的版权、许可证和来源见 [NOTICE](NOTICE)；对应源码与授权核查尚未全部完成，具体事项见[第三方材料状态](docs/compliance/native-status.md)。
