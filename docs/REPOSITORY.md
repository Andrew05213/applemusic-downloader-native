# 仓库结构与维护边界

当前默认入口为 Native 1.0 原生版，尚未发布本仓库安装包。
源码来自旧项目 2.0 的最新快照，但 Git 历史从新根提交开始，详见[迁移边界](development/native-1.0-migration.md)。

## 源码导航

| 路径 | 职责 |
| --- | --- |
| `src/main/native-main.js` | Electron 主入口、窗口与受控 IPC |
| `src/preload/native-*.js` | 原生界面的权限桥接 |
| `src/renderer/native-*` | 自有界面、启动画面和播放器 |
| `electron/native-*.js`、`build-native-*`、`dist-native.js` | 原生开发、构建、缓存与验证工具 |
| `apple-music-downloader/` | 共享 Go 后端、队列、下载、文件处理和桌面适配 |
| `apple-music-downloader/internal/` | guest 通信、原生登录与运行组件管理等 |
| `apple-music-downloader/internal/service/` | 不依赖传输/存储实现的队列、执行器与生命周期接口 |
| `apple-music-downloader/internal/legacyweb/` | 旧 CLI/Web 的 Wrapper 启动策略，不供原生 guest 登录使用 |
| `native-runtime/` | Lite/QEMU 锁定输入、构建配方与源码材料收集工具 |
| `test/`、`e2e/` | 单元、UI 与隔离程序验收 |
| `docs/` | 当前指南与明确标注的历史记录 |
| `scripts/` | 仓库维护脚本，不随应用分发 |
| `web/` | 共享 Go/Web 兼容界面与相关回归，不进入原生安装包 |
| `legacy/v1/` | 冻结的 1.x 桌面、WSL/DRM 工具和专属测试；仅供查阅 |

## 旧代码边界

旧 Electron 主进程、preload、WSL/DRM 工具、Docker/Web 启动助手及其专属测试已移入
`legacy/v1/`；99 个文件的原路径和 SHA-256 记录在 `legacy/v1/manifest.json`。
归档保留原始内容，不是独立可运行的 1.x 工程，不要在归档目录中直接运行 npm 或旧脚本。

旧仓库中的完整 1.4.3 维护入口为 `codex/maintenance-1.x`，固定基线
`c0719fc61b7a553348e8d7be96321cc37eb0a0d9`。独立检出方式见[旧版维护入口](legacy/README.md#维护-1x)。

第二阶段将 242 个旧 Appliance/Wrapper 文件与专属 Go 包、命令继续归档，并将
9 个共享 Web 资源独立到 `web/`。`src/renderer/` 只保留原生界面。
`appliance/release-policy.json` 仍是打包与校验输入，不移动；Go 根包仍编译部分
CLI/Web 兼容代码，不能把目录整理当作完整的代码解耦。
具体依赖和后续拆分限制见[第二阶段依赖边界](development/dependency-boundaries.md)。
第三阶段已先定义共享接口，拆出旧 Web HTTP 适配与 Wrapper 宿主配置，详见
[共享 Go 接口与宿主适配](development/shared-go-interfaces.md)。
第四阶段将 JobAPI 改为消费用例接口；`job_api_composition.go` 绑定具体实现，
`job_file_service.go` 负责文件身份、句柄和删除准备。领域模型、存储与 Wrapper 进程核心仍在主包，
详见[JobAPI 与文件用例边界](development/job-api-file-boundaries.md)。
移动源码、许可证或锁定输入前，必须核查构建白名单、固定路径和哈希验证，不能为整理目录破坏来源校验。

## 本地产物与其他工作区

构建产物、缓存、打包阶段和归档不提交 Git，位置及清理规则见[构建存储](development/build-storage.md)。
文档中的历史工作树路径不是要求贡献者复制的目录结构。
其他工作树可能有独立的未提交修改，不能视为重复备份直接删除；文档治理不授权清理代码或账号资料。
