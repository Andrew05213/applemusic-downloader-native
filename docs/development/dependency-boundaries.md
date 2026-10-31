# 第二阶段依赖边界

基线为 `af222179ed07f0b128fd09a2bb35cdcb4bb626ac`。本轮拆分目录和旧辅助程序，
不迁移用户资料，不更换 runtime 输入，不删除上游内置设备材料，也不移动 `v2.0.0` 标签。

## 2.0 运行与构建

| 入口 | 当前职责与实际依赖 | 不依赖的旧内容 |
| --- | --- | --- |
| `src/main/native-main.js`、`src/renderer/native-*` | 原生窗口、IPC、自有界面与播放器；安装器采用明确白名单 | 共享 `web/`、旧 Electron/网页壳 |
| Go 根包 `.` → `amdl-service.exe` | 队列、目录、文件/标签、原生登录适配、下载和分片播放；仍编译共享 CLI/Web 兼容代码 | `internal/appliance` 与三个旧 WSL 命令 |
| `cmd/amdl-desktop` | 诊断控制后端，依赖 `nativeruntime`、`guestclient`、`nativeauth`、`wrapperlite` | 旧 WSL 探针/引导程序 |
| `cmd/amdl-guest` | guest agent 与原生会话协议，依赖 `guestagent`、`nativeauth` | 旧 Appliance supervisor |
| `native-runtime/` 与 `electron/build-native-runtime.js` | 锁定 Lite 源码归档、QEMU 输入、工具链镜像和 guest 源码 | 仓库内旧 `wrapper-linux/` 的源码/rootfs |

Go 根包尚未拆成独立的原生与 CLI/Web 模块。`server.go`、`wrapper_manager.go`、
共享 API 和 `utils/runv3` 等兼容代码仍会参与根包编译；不能宣称所有旧代码已从服务 EXE 中移除。
原生歌曲下载走 `native_download.go` → `runv2` → guest decryptor；播放走
`native_playback.go` → `runv2.OpenStream`，与旧 Web/Wrapper 控制路径分开。
本轮未改动这些媒体实现和 guest 锁定输入。

## 旧资料与必须保留的边界

- `legacy/v1/appliance/`：旧配方、版本/包锁、补丁、探针、overlay、脚本与 Python/Go 相关历史资料。
- `legacy/v1/wrapper-linux/`：旧源码、Makefile、预编译程序和 rootfs 参考输入；字节不变。
- `legacy/v1/apple-music-downloader/`：旧 `internal/appliance`，以及 `amdl-appliance`、
  `amdl-windows-probe`、`amdl-wslinterop-bootstrap` 三个命令。它们已离开当前 Go 模块的编译/测试发现范围。
- `appliance/release-policy.json`：**仍保留原路径**，因为原生安装器携带该关闭的旧策略，
  原生 CI/打包测试也校验它。它不是原生运行后端，不可当作冗余文件删除或擅自打开。
- `native-runtime/inputs.lock.json` 的工具链镜像仍有历史来源；归档配方不等于重建了该镜像，
  也不消除其来源、许可证或可复现性核查要求。当前驱动验证固定镜像 ID。

本轮 242 个旧文件的移动前路径、归档路径和 SHA-256 见
[第二阶段清单](../../legacy/v1/phase2-manifest.json)。两份原目录 README 保留为导航入口，
其归档副本仍保留基线内容。第一阶段 99 文件清单单独保留，不改写旧记录。
这里只移动已跟踪文件；被忽略的账号目录、本地产物和缓存不随目录整理移动或删除。
完整 1.x 维护仍须[独立检出维护分支](../legacy/README.md#维护-1x)，归档不是独立构建树。

## 共享 Web 与回归

9 个共享前端资源原样移到 [web/](../../web/README.md)，`src/renderer/` 只保留原生资源。
Go `serve` 默认路径改为 `../web`，服务支持仓库根目录和 Go 模块目录启动，
保留显式 `--static-dir`，不隐式暴露原生 renderer。
Web 的任务状态/SSE、浏览队列和数据回归继续执行，只更新资源路径；没有通过删除共享测试来消除失败。
旧补丁启动标记和库指纹守卫仍会读取归档锁文件，属于**兼容证据测试**，不是原生 runtime 的构建依赖。

```powershell
npm run verify:native
npm run verify:boundaries
npm test
npm run test:web
node scripts/check-docs.js --all
```

`verify:boundaries` 对 Windows service/control 和 Linux guest 逐个执行只读 `go list -deps`，
拒绝重新引入归档的 Appliance/WSL 包；本地缓存写在仓库卷，CI 两个平台都执行。
Go 全量测试仍覆盖当前模块，不再包含已归档的旧专属包。真实 AAC 夹具和跨平台测试前置
见[测试入口](../testing/README.md)。

## 留待后续

后续接口与首批消费者迁移见[第三阶段说明](shared-go-interfaces.md)；本页保留第二阶段的验收边界。

若继续拆分 Go 根包，应先定义队列、API、文件与媒体处理接口，再迁移 CLI/Web 和旧 Wrapper
生命周期；不能直接移动整个 `main.go`、`server.go` 或删除仍被原生路径调用的共享函数。
本轮不使用 build tags 隐藏共享安全回归，也不更改安装标识、会话位置或发布附件。
