# Apple Music Downloader Native

Windows 原生 Apple Music 客户端与下载工具。新仓库从 **1.0.0** 开始；沿用旧项目 2.0 的原生实现，不带入旧 Git 提交历史。

界面、登录、队列和文件管理在本机运行；媒体能力由随包携带的 Lite/QEMU 组件提供，用户无需安装 WSL、Docker 或使用内置官方网页。

> 第三方源码、许可证及授权核查仍未完成。只访问和保存你有权使用的内容，详见[核查清单](docs/compliance/native-2.0-status.md)。本项目与 Apple 无隶属关系。

## 下载与安装

本仓库目前提供源码，**尚未发布 1.0.0 安装包或 Release**。后续安装包将在[本仓库 Releases](https://github.com/Andrew05213/applemusic-downloader-native/releases)提供，Windows x64，安装向导支持选择位置。

正式提供安装包时会同时提供 SHA-256。不要将旧仓库的 2.0.0 安装包、哈希或源码附件视为本仓库的 1.0.0 产物。

## 功能与边界

- 单一播放入口，AAC 分片流式播放；不可用时明确回退到试听。
- 下载独立运行，默认 ALAC，支持任务管理、保存目录、文件名模板和缓存调整。
- 原生设备验证码登录；下载、完整播放和资源可用性受账号、订阅及地区限制。
- 自动测试不等于所有真实账号或地区均已验收。

## 从哪里开始

- [开发与构建](docs/development/README.md)：准备环境、源码运行和构建前置。
- [用户指南](docs/USER-GUIDE.md)：登录、播放、下载、设置与诊断。
- [CI 与发布流程](docs/RELEASE.md)：跨平台验证和显式审核后发包。
- [文档导航](docs/README.md)、[仓库结构](docs/REPOSITORY.md)、[源码基线与兼容说明](docs/development/native-1.0-migration.md)。

旧仓库的标签、Release 和维护分支未移动。必要的历史来源材料仍保存在源码树中，并明确标为归档；新仓库不包含旧分支或旧标签。
