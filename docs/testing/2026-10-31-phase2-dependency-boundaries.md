# 第二阶段依赖隔离验收

日期：2026-10-31。起点提交：`af222179ed07f0b128fd09a2bb35cdcb4bb626ac`。
本记录描述本地工作树验证，不代表新的安装包或 Release 已发布。
实现边界见[依赖说明](../development/dependency-boundaries.md)。

## 变更范围

- 242 个已跟踪旧文件归档：Appliance 配方/锁/测试/overlay、旧 Wrapper/rootfs，
  以及 Go `internal/appliance` 和三个专属 WSL 命令。两份旧 README 原文归档，原位置留导航。
- 9 个共享 Web 资源原样移到 `web/`；更新 Go serve 默认路径、静态目录发现和共享前端测试引用。
- 保留 `appliance/release-policy.json` 的原路径与关闭状态，原生打包仍携带并校验它。
- 原生 UI、下载、分片播放、runv2、会话/guest 源码和 runtime 锁定输入中的 64 个关键文件与基线字节一致。
- 不移除上游 CDM 设备材料，不更换运行组件，不迁移账号或清理被忽略的数据。

## 实际执行

| 检查 | 结果与范围 |
| --- | --- |
| 原生/共享 Node 单元测试 | 194 项通过，包含保留的共享 Web 回归和新增依赖/归档守卫 |
| UI 回归 | 122 项通过，使用已有无账号 AAC 夹具，含真实分片播放、拖动、切歌及下载入口独立性 |
| Go 原生依赖 | 三个实际 service/control/guest 目标的项目内包依赖集合与起点一致，无旧 Appliance/WSL 包 |
| Windows Go 1.25.0 | 当前模块测试、race、vet 通过；race 沿用 CI 对 `^TestWrapperSession` 的 directory-Sync 平台排除 |
| Linux Go 1.25.0 | 隔离 tmpfs 内 `go test -count=1 -race ./...`、`go vet ./...` 通过；没有 Windows 的用例排除 |
| 原生构建 | Windows service/control 与 Linux guest 构建通过，输出到 E 盘本地证据目录 |
| 移动文件 | 242 个归档文件及 9 个 Web 资源的 SHA-256 全部匹配；独立索引下 LF/CRLF 两种检出再次通过 |
| 原生资源边界 | 安装白名单仍不含 Web/归档资源；保留 runtime 的源码来源校验通过，既有正式包的 198 资源回执审核通过 |
| 工作流/文档 | actionlint、当前本地链接/标题锚点、`git diff --check` 通过 |

新 Web 路径测试最初被完整 vet 检出使用高于模块声明版本的 `testing.Chdir`；
已改用兼容 Go 1.23 API 的目录恢复辅助方法，并重跑跨平台检查，未提升模块版本或跳过失败。

## 证据与限制

本机日志、编译输出和前后依赖 JSON 位于被 Git 忽略的 `.artifacts/phase2-boundaries/`，
不是公开下载地址；该目录不应上传账号资料。独立验证索引未改动真实暂存区。
移动清单分别为 `legacy/v1/manifest.json`（第一阶段）和
`legacy/v1/phase2-manifest.json`（本阶段），不改写先前隔离证据。

本轮未执行真实 Apple 账号登录、订阅验证或商业媒体下载；UI/媒体检查使用合成夹具。
上述结果为提交前的本地验收，远端 CI 状态以对应提交的实际运行结果为准。
未创建新的 NSIS 安装包，未改动 `v2.0.0` 标签与附件。
当前 Go 根包仍编译 CLI/Web 兼容代码；本轮不是完整的 Go 模块拆分或第三方授权验收。

## 合入前 CI 补充修复

第二阶段分支首次[远端验证](https://github.com/Andrew05213/applemusic_downloader/actions/runs/41198720237)
中，Linux 全部通过；Windows 的 `TestRunDownloadCommandRedactsCredentialLikeFailureDetails`
在清理临时 `jobs.db` 时报告文件仍被占用，main 未因此更新。

本地重复原用例 100 次未复现该偶发失败；进一步用可控阻塞驱动模拟取消事务回滚，
确定性验证了 `sql.DB.Close` 可在已借出连接完成清理前返回。
新增 `TestCLIStoreCloseDrainsCancelledTransactionBeforeReturning` 在原关闭方式下失败，
修复后与原用例各执行 100 轮 race 检查通过。

修复仅在 CLI worker 退出后取得并归还单连接池连接，再关闭 store；等待有超时边界。
没有更换 SQLite、放宽目录安全检查或跳过失败用例，也没有修改原生下载、播放及 guest 输入。
后续远端验证状态以修复提交的实际工作流为准。
