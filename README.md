<h1 align="center">Apple Music Downloader Native</h1>

<p align="center">用于浏览 Apple Music、播放音乐和管理下载的 Windows 桌面工具。提供原生登录界面、ALAC 下载与分片流式播放，无需安装 WSL、Docker 或使用内置官方网页。</p>

<p align="center">
  <a href="https://github.com/Andrew05213/applemusic-downloader-native/actions/workflows/verify.yml"><img alt="Native Verify" src="https://img.shields.io/github/actions/workflow/status/Andrew05213/applemusic-downloader-native/verify.yml?branch=main&style=flat-square&label=Native%20Verify&labelColor=2B3245"></a>
  <a href="https://github.com/Andrew05213/applemusic-downloader-native/releases/latest"><img alt="Windows x64" src="https://img.shields.io/badge/Windows-x64-4B67B4?style=flat-square&labelColor=2B3245"></a>
  <a href="LICENSE"><img alt="许可证 Apache-2.0" src="https://img.shields.io/badge/%E8%AE%B8%E5%8F%AF%E8%AF%81-Apache--2.0-4B67B4?style=flat-square&labelColor=2B3245"></a>
  <a href="https://github.com/Andrew05213/applemusic-downloader-native/issues"><img alt="问题反馈" src="https://img.shields.io/badge/%E9%97%AE%E9%A2%98%E5%8F%8D%E9%A6%88-GitHub%20Issues-4B67B4?style=flat-square&logo=github&logoColor=white&labelColor=2B3245"></a>
  <a href="https://qun.qq.com/universal-share/share?ac=1&amp;authKey=aRFZEm8UHKTmbnDbXMIS9Pb71v0OtSxD%2FEnrQXar%2Bg3%2FkZfYZQKMVCRleIwcV13Q&amp;busi_data=eyJncm91cENvZGUiOiIxMTMxMjc3MDM2IiwidG9rZW4iOiJubVVESUNBODJRSkQ3Q2xMK2VpZWtudjFaNEFlNkloc3ZNVGlaT2hvVWZCVzkwYWJ6MU9ocWJwRExFcFFPQWZzIiwidWluIjoiMzEyOTk0NjcwMSJ9&amp;data=CPyNCmgNvrBf-z0RY3PcYqswhr2yYWEGmBocySkcnBszLAjpuZckyrAk9wups58lj6uLA26mDcYhj1Q9TjhHNw&amp;svctype=4&amp;tempid=h5_group_info"><img alt="QQ 群 1131277036" src="https://img.shields.io/badge/QQ%20%E7%BE%A4-1131277036-4B67B4?style=flat-square&logo=qq&logoColor=white&labelColor=2B3245"></a>
</p>

<p align="center">
  <a href="https://github.com/Andrew05213/applemusic-downloader-native/releases/tag/v1.0.3">下载 1.0.3 正式版</a> · <a href="CHANGELOG.md">更新日志</a> · <a href="docs/USER-GUIDE.md">使用指南</a> · <a href="docs/development/README.md">开发文档</a> · <a href="https://github.com/Andrew05213/applemusic-downloader-native/issues">问题反馈</a>
</p>

## 功能

- **浏览与搜索**：首页推荐、新发现、歌单、专辑和音乐人详情，支持页面缓存与预加载。
- **播放**：AAC 分片流式播放，支持进度拖动；完整播放不可用时回退到试听。
- **下载**：默认 ALAC，支持并行队列、实时速度与进度、失败重试，以及原生动态封面下载。
- **文件管理**：选择保存位置、点击生成文件名模板、定位文件夹，分别支持仅删除任务或同时删除文件。
- **登录**：原生界面展示 Apple 验证提示、按钮和必要输入，支持受信任设备验证码、重试与取消；手机号可选择国家区号，默认中国大陆 `+86`，已有国际区号不会重复添加，邮箱保持原样。
- **界面**：窗口宽度为 900px 及以下时自动收为图标导航栏，拉宽后按原有展开偏好恢复；窄窗口搜索使用独立内容行，不遮挡正文。

播放、下载和目录可用性取决于账号授权、订阅与地区；部分资源可能不可用。

## 下载与安装

当前正式版为 **1.0.3**，适用于 Windows x64：

- [下载安装包](https://github.com/Andrew05213/applemusic-downloader-native/releases/download/v1.0.3/AppleMusicDownloader-1.0.3-Setup.exe)
- [下载 SHA-256 校验文件](https://github.com/Andrew05213/applemusic-downloader-native/releases/download/v1.0.3/AppleMusicDownloader-1.0.3-Setup.exe.sha256)
- [1.0.3 发布说明](https://github.com/Andrew05213/applemusic-downloader-native/releases/tag/v1.0.3) · [更新日志](CHANGELOG.md) · [历史版本](https://github.com/Andrew05213/applemusic-downloader-native/releases)

1.0.3 整合原生认证交互、手机号区号、原生动态封面下载、新应用与下载图标，并修复窄窗口遮挡和设置齿轮显示。成品用户无需额外安装 WSL、Docker 或 FFmpeg。

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

- [更新日志](CHANGELOG.md)
- [仓库结构](docs/REPOSITORY.md)与[后端接口边界](docs/development/job-api-file-boundaries.md)
- [构建与发布流程](docs/RELEASE.md)
- [依赖与上游来源](docs/UPSTREAMS.md)
- [完整文档导航](docs/README.md)

## 直接上游

- [zhaarey/apple-music-downloader](https://github.com/zhaarey/apple-music-downloader)：本项目下载器的直接上游，原始脚本署名 Sorrow。
- [Andrew05213/wrapper（Wrapper Lite 维护分支）](https://github.com/Andrew05213/wrapper)：提供 Lite 服务，1.0.3 固定使用提交 [b2bebc3](https://github.com/Andrew05213/wrapper/commit/b2bebc318ae9e7aa208a27642c193d3a79418d95)；源自 [WorldObservationLog/wrapper](https://github.com/WorldObservationLog/wrapper)，保留上游 [MIT License](https://github.com/WorldObservationLog/wrapper/blob/2fd7e0eaef081f93cf687428b965674937832ba4/LICENSE) 声明。

感谢上述项目的作者与贡献者。各上游代码保留其原有版权和适用许可；版本及许可记录见[上游来源](docs/UPSTREAMS.md)。

## 许可证

本项目由维护者拥有版权或有权授权的原创代码及修改采用 [Apache License 2.0](LICENSE)。Copyright 2026 Andrew05213。

第三方导入代码、历史归档、依赖及运行组件不因根目录许可证而改变授权；原有版权和许可证声明继续保留。来源或授权尚未确认的材料也不因此获得再分发许可，范围和说明见 [NOTICE](NOTICE)。

## 第三方说明

本项目与 Apple 无隶属关系。请只访问和保存你有权使用的内容。

第三方组件的版权、许可证和来源见 [NOTICE](NOTICE)；对应源码与授权核查尚未全部完成，具体事项见[第三方材料状态](docs/compliance/native-status.md)。
