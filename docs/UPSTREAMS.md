# 上游源码与依赖来源

本仓库维护 Native Windows 桌面版。组件身份以锁定输入和实际二进制为准，不将上游默认分支的许可证推定为全部历史提交或随包材料的授权。

| 组件 | 来源与身份依据 |
| --- | --- |
| 原始下载器 | [zhaarey/apple-music-downloader](https://github.com/zhaarey/apple-music-downloader)；历史 Gitlink 为 `4bd24bf5374053fbab070f61ee7ae2addcf589bc`，当前作为普通源码维护；原脚本署名 Sorrow，完整来源和再分发授权仍待确认 |
| Wrapper Lite | [WorldObservationLog/wrapper](https://github.com/WorldObservationLog/wrapper)，commit `2fd7e0eaef081f93cf687428b965674937832ba4`；归档哈希、补丁和工具链身份在 native-runtime |
| QEMU Windows | 11.1.0，发行者安装器及 SHA-512 在 native-runtime/inputs.lock.json；具体源码对应性、DLL/固件许可待核查 |
| Debian Linux / BusyBox | 固定快照与二进制包版本在同一锁文件；部分源码资料包不等于完整对应源码提供 |
| cJSON / Dobby | commit 分别为 `c859b25da02955fef659d658b8f324b5cde87be3`、`9d271803881f467aabfc24593c7dcaeb710fe861`，许可证在 third-party-licenses |
| Electron | 标准 Electron 45.0.0，以 package.json/package-lock.json 为准，不是 Castlabs DRM 运行时 |
| 开发用 7-Zip | 官方 25.01 归档、EXE/DLL 与许可证哈希在 native-runtime/build-tools.lock.json；仅开发解包使用，不运行安装器或随成品分发 |
| Go / npm 依赖 | Go 模块以 go.mod、go.sum 和实际二进制 build info 为准；npm 依赖以根锁文件为准 |

当前界面和播放器不加载官方 Apple 网页，也不通过网页 Cookie 抓取登录。工具链镜像的取得与可复现性限制见[开发指南](development/README.md)。

## 项目来源与兼容资料

本仓库初始源码导入前身 `Andrew05213/applemusic_downloader` 的快照 `81e5c388347478b19e99137777abab33e769ca61`，建立独立 Git 历史。维护当前源码无需访问旧仓库。

`legacy/v1/` 保留旧桌面、WSL、Appliance 和 Wrapper 参考资料，部分锁文件仍用于兼容回归。归档不是当前 runtime 构建输入；原始声明和哈希不因整理文档改写，见[归档说明](legacy/README.md)。

Go 模块、安装身份和资料目录中的旧名称属于[兼容标识](development/compatibility.md)，不是当前版本号。

## 更新依赖

更新上游、版本或补丁时，同时核对不可变来源、许可文本、构建配置、二进制身份和对应源码；重新构建并验证受影响组件，不修改历史证据冒充旧产物已获验证。

第三方材料和未完成项见 [NOTICE](../NOTICE)及[核查状态](compliance/native-status.md)，发行操作见[发布指南](RELEASE.md)。
