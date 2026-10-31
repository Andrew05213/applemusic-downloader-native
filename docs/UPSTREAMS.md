# 上游源码与依赖来源

本文以 Native 1.0 的原生构建为主。版本依据是锁定输入，不把默认分支的许可状态推断为旧提交或所有随包文件的授权。
本项目源码托管于 [Andrew05213/applemusic-downloader-native](https://github.com/Andrew05213/applemusic-downloader-native)，承接旧仓库 2.0 代码，见[迁移来源](development/native-1.0-migration.md)。
第三方权利与对应源码的未完成事项见 [NOTICE](../NOTICE) 和[核查状态](compliance/native-2.0-status.md)。

## 当前原生链路

| 组件 | 来源和身份依据 |
| --- | --- |
| 原始下载器 | [zhaarey/apple-music-downloader](https://github.com/zhaarey/apple-music-downloader)；历史 Gitlink 为 `4bd24bf5374053fbab070f61ee7ae2addcf589bc`，当前作为普通源码维护；原脚本署名 Sorrow。完整导入来源及再分发授权仍待确认。 |
| Wrapper Lite | [WorldObservationLog/wrapper](https://github.com/WorldObservationLog/wrapper)，提交 `2fd7e0eaef081f93cf687428b965674937832ba4`；归档哈希、工具链及本地补丁见 `native-runtime/inputs.lock.json` 和该目录构建资料。 |
| QEMU Windows | 11.1.0，发行者安装包及 SHA-512 见同一锁文件；具体源码提交、DLL 和固件授权仍待核对。 |
| Debian Linux / BusyBox | 锁定快照与二进制包版本见同一锁文件；已收集的部分源码资料不等于完整对应源码提供。 |
| cJSON / Dobby | 固定提交分别为 `c859b25da02955fef659d658b8f324b5cde87be3`、`9d271803881f467aabfc24593c7dcaeb710fe861`；许可证文本见 `third-party-licenses/`。 |
| Electron | 标准 Electron 45.0.0；以 `package.json`、`package-lock.json` 为准，不是 Castlabs DRM 运行时。 |
| Go / npm 依赖 | Go 模块以 `go.mod`、`go.sum` 及实际二进制 build info 为依据；npm 依赖以根目录锁文件为依据。清单及候选许可证不等于已逐文件审查。 |

当前界面和播放器不加载官方 Apple 网页，也不通过网页 Cookie 抓取登录。
公开构建开关属于维护者配置，不能当作第三方授权。

## 旧版本

1.x Docker/WSL 构建当时使用 `appliance/versions.lock` 和旧 Wrapper 配方，现归档在
`legacy/v1/appliance/` 与 `legacy/v1/wrapper-linux/`；
旧浏览器桌面曾使用 Castlabs Electron ECS。它们的来源与命令仅作为[历史资料](legacy/README.md)，不能套用到 2.0。
相关旧桌面脚本和配置已隔离到 `legacy/v1/`；完整旧版本在 `codex/maintenance-1.x`
维护，不能把归档目录当作当前构建入口。原生 runtime 的锁文件与源码输入未移动，
`appliance/release-policy.json` 仍作为旧策略兼容资料进入原生打包和校验。
历史 NOTICE 保留在 `docs/legacy/NOTICE-v1`，其锁定哈希及旧构建引用独立维护。

## 更新来源

修改上游、版本或补丁时，同时审查不可变来源、许可文本、构建配置、二进制身份和对应源码。
重新构建并验证受影响组件；不得改写历史证据来冒充旧产物已获验证。
当前运行组件构建说明见[开发入口](development/README.md)，发布操作见[发布流程](RELEASE.md)。
