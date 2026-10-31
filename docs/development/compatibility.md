# 兼容标识与用户资料

本仓库产品版本从 Native 1.0.0 开始。部分源码和安装标识沿用前身项目，不能将标识中的旧版本号当作产品版本，也不能为了统一文档而直接更名。

## 保留的标识

- Go module 为 `github.com/Andrew05213/applemusic_downloader`。本地构建使用仓库内源码，不需要下载旧私有仓库；修改模块路径会影响源码身份和锁定 guest 验证。
- appId 为 `io.github.andrew05213.applemusicdownloader.v2test`，NSIS GUID 为 `762d2d31-e8dd-4aca-a7e5-89dbd09cb138`。
- 用户资料目录保留 `AppleMusicDownloader-v2-test` 名称；默认媒体目录的兼容名称为 `Apple Music Downloads 2.0 Test`。已保存的下载目录优先。
- 开发工具仍使用 `.tmp-v2-native/` 作为缓存与临时目录。
- `legacy/v1/` 指前身旧桌面/WSL 源码，不是 Native 1.0。

## 数据和安装边界

版本重新计数不会自动迁移、清理账号、任务或媒体。卸载保留用户资料与下载文件。不要在诊断反馈中上传资料目录或凭据。

从前身 2.0.0 覆盖安装 Native 1.0.0 涉及降版本处理；保留相同安装标识不等于该迁移已验收。旧版用户应先备份，现有验证范围见[1.0.0 发布记录](../releases/1.0.0.md)。

Windows 运行资料路径过深时，QEMU 可能无法读取 BIOS；验收中曾复现 265 字符的 BIOS 路径失败。测试和部署应使用常规长度目录，不绕过文件身份或来源检查。

## 来源

本仓库初始源码来自前身项目快照，来源见[上游清单](../UPSTREAMS.md)。旧仓库分支、标签和 Release 与本仓库独立；当前发布只使用本仓库标签和附件。
