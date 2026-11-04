# 仓库结构

本仓库维护 Windows Native 桌面版。使用入口见[用户指南](USER-GUIDE.md)，构建入口见[开发指南](development/README.md)。

| 路径 | 职责 |
| --- | --- |
| `src/main/native-main.js` | Electron 主入口、窗口与受控 IPC |
| `src/preload/native-*.js` | 界面权限桥接 |
| `src/renderer/native-*` | 自有界面、启动画面和播放器 |
| `electron/` | 原生运行、构建、缓存、打包和验证工具 |
| `apple-music-downloader/` | Go 服务、队列、下载、播放与文件处理 |
| `apple-music-downloader/internal/service/` | 共享应用接口和有序退出 |
| `apple-music-downloader/internal/guestagent/` | guest 控制与媒体协议 |
| `apple-music-downloader/internal/nativeauth/` | 原生认证适配 |
| `apple-music-downloader/internal/legacyweb/` | 旧 Web 的 Wrapper 启动策略 |
| `native-runtime/` | Lite/QEMU 锁定输入、构建配方、补丁和源码材料工具 |
| `test/`、`e2e/` | 单元、UI 与隔离应用测试 |
| `web/` | Go serve 兼容界面，不进入桌面安装包 |
| `scripts/` | 文档、边界与本地构建存储维护 |
| `scripts/native-dev.js` | 环境检查、工作区 runtime 选择与开发/打包入口 |
| `scripts/legacy-binaries.js` | 可选旧二进制附件获取与校验，不参与 Native 构建 |
| `native-runtime/toolchain/` | 从公开锁定输入构建 Linux 工具链的配方 |
| `docs/` | 用户、开发、架构、测试与发布文档 |
| `legacy/v1/` | 旧桌面/WSL 代码、工具和专属测试的冻结参考 |

## 兼容层与保留输入

Go 根包仍编译部分 CLI/Web 和旧 Wrapper 代码，并非独立的纯原生模块。桌面下载和播放使用独立的 guest 媒体路径；不要根据文件名批量删除共享实现。

`appliance/release-policy.json` 是打包与校验仍需携带的关闭旧策略，不是当前运行后端。原生策略位于 `native-runtime/release-policy.json`。锁文件、配方、原始许可证和归档清单都是来源验证输入，移动前应检查引用及哈希。

详见[依赖边界](development/dependency-boundaries.md)、[共享接口](development/shared-go-interfaces.md)及[JobAPI 文件边界](development/job-api-file-boundaries.md)。

## 归档与生成文件

`legacy/v1/` 不是独立可运行的产品，也不是本仓库 Native 1.0。用途和校验清单见[旧源码说明](legacy/README.md)。

旧 `rootfs/system` 预编译程序与库从当前 Git 树外置到 Release，身份记录在 `legacy/v1/binary-assets.lock.json`，原始归档哈希清单保持不变。旧 Git 历史未重写；新贡献者建议从[浅克隆](../CONTRIBUTING.md)开始。

构建产物和缓存不提交 Git；位置与安全清理规则见[构建存储](development/build-storage.md)。安装身份、Go 模块和用户资料的兼容名称见[兼容说明](development/compatibility.md)。
