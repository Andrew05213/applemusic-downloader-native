# 第三阶段共享 Go 接口验收

日期：2026-10-31。起点提交：`fafa84ca134665f86a3f4ebc2720adb3ea402dec`。
本记录对应本地 `codex/shared-go-ports` 工作树，不代表已合并、推送或发布。
实现和剩余边界见[共享接口说明](../development/shared-go-interfaces.md)。

## 变更

- 增加不依赖传输/存储实现的 `internal/service` 接口：执行、查询、提交、取消、事件、启动和等待。
- 保留 Job 模型与序列化格式，以类型绑定和编译期断言接入现有执行器/队列。
- CLI 等待/取消和旧 Web 提交/SSE/Wrapper 状态/验证码消费窄接口，使用无数据库 fake 验证。
- 从 `server.go` 分出兼容 HTTP 处理器和服务生命周期；从 `wrapper_manager.go` 分出宿主配置。
  旧 Wrapper 是否启动的策略进入 `internal/legacyweb`；进程管理及安全状态机不搬迁。
- 统一取消 → 等待 → 关闭的顺序，保留 CLI 连接排空修复和 typed-nil 适配。
- 增加接口依赖、原生入口不接入旧宿主、维护模式短路凭据检查等架构/契约测试。

## 实际验证

| 检查 | 结果与边界 |
| --- | --- |
| Node 原生/共享单元 | 194 项通过 |
| Playwright UI | 122 项通过；设置真实 AAC 夹具，包含分片播放、拖动和切歌回归 |
| Windows Go 1.25.0 | `go test -count=1 -race -skip '^TestWrapperSession' ./...`、`go vet ./...` 通过；沿用 CI 的 directory-Sync 平台排除，没有新增排除 |
| Linux Go 1.25.0 | 独立 tmpfs 内 `go test -count=1 -race ./...`、`go vet ./...` 通过，没有 Windows 的用例排除；测试容器禁止联网 |
| 新接口/有序退出 | 相关 CLI、Web/SSE、Wrapper 挑战与共享 shutdown 用例另执行 50 轮 race，通过 |
| CLI 退出回归 | 原凭据失败脱敏用例和取消事务连接排空用例各执行 100 轮 race，通过 |
| 三目标构建 | Windows service/control、Linux guest 构建通过；未组装安装包 |
| 原生契约与依赖 | `verify:native`、`verify:boundaries` 通过；control/guest 不依赖旧 Web，service 根包仍编译兼容实现 |
| 关键源码 | 64 个原生 UI、下载、分片播放、runv2、guest/登录和 runtime 输入文件与起点字节一致 |
| runtime 来源 | 既有 bundle 的源码/锁定输入验证通过，不代表本轮重新打包或重新发布 |
| 文档与格式 | 本地链接/标题锚点检查及 `git diff --check` 通过 |

新增 HTTP fake 测试最初遗漏 `Content-Type: application/json`，被现有请求校验拒绝；
已修正测试请求并重跑跨平台检查，没有放宽生产校验。
直接在 Windows 执行不带平台排除的全量测试仍有既有 directory-Sync 限制；
这些 Wrapper 会话测试在本轮 Linux 全量 race 中通过。

## 证据及未验证范围

本机日志与构建输出在被忽略的 `.artifacts/phase3-boundaries/`，不应提交这些本地产物。
测试使用隔离数据和合成 AAC，不读取真实账号、不启动真实 Wrapper，不登录或下载商业媒体。
未修改已发布 `v2.0.0` 标签、附件、账号目录和运行材料。

结束时只读核对远端：main 为 `fafa84ca134665f86a3f4ebc2720adb3ea402dec`，
`v2.0.0` 标签对象为 `0932b1b2fae607c620188a158b79d220f59b86c7`，
指向提交 `9a30dbc6f377a4954448d3368220317fda3131dd`。本阶段尚无远端 CI 结果。

根包的 `JobAPI`/store 私有文件身份耦合及旧 Wrapper 进程核心仍在；不能宣称原生 EXE 已完全移除旧代码。
后续拆分应继续保留安全与媒体回归，而不是通过删测试或隐藏编译入口消除依赖。
