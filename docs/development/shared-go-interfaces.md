# 第三阶段：共享 Go 接口与宿主适配

基线为 `fafa84ca134665f86a3f4ebc2720adb3ea402dec`。本阶段先建立接口，再迁移少量消费者；
不改变 API/SQLite 格式、文件身份校验、安装标识、账号目录和 `v2.0.0` 标签。

本页记录第三阶段边界；后续 JobAPI 的迁移和当前剩余耦合见[第四阶段说明](job-api-file-boundaries.md)。

## 新边界

下列路径均相对 `apple-music-downloader/`：

| 层 | 路径 | 约束与首批消费者 |
| --- | --- | --- |
| 共享应用接口 | `internal/service/ports.go` | 执行、查询、提交、取消、事件读取及启动/等待；不依赖 HTTP、SQL、文件或进程实现 |
| 有序退出 | `internal/service/lifecycle.go` | 取消 worker → 使用独立上下文等待退出 → 关闭存储；保留错误链 |
| 主包类型绑定 | `service_ports.go` | 用现有 Job/事件类型实例化接口，并在编译期检查实现；不复制安全字段 |
| CLI 消费者 | `cli_runtime.go` | 等待只需查询接口，取消只需取消接口；仍由原有队列执行任务 |
| 旧 Web 适配 | `legacy_web_http.go` | 提交、只读 SSE、Wrapper 状态/验证码分别接收窄接口；保持旧响应和弃用标头 |
| 旧 Wrapper 策略 | `internal/legacyweb/wrapper.go` | 只决定是否启动；维护模式短路凭据检查，缺少凭据不启动，配置完整时传递原上下文和错误 |
| 旧 Wrapper 宿主 | `legacy_wrapper_host.go` | 保留环境/继承凭据优先级、secret-fd、loopback、会话路径与日志脱敏配置 |
| 现有服务装配 | `server.go`、`service_lifecycle.go` | 继续绑定路由、鉴权、持久化和具体实现；关闭适配处理 typed-nil |

接口使用类型参数，是为了保留当前领域类型，而不是用 `any`/JSON 传递未检查的数据。
`JobExecutor` 是原签名的实例化别名；现有执行器和原生调用点无需修改。
SSE 接口没有提交或取消能力，不能因读取事件顺带创建下载任务。

## 不变的安全和媒体边界

- `native_desktop.go`、`native_download.go`、`native_playback.go` 不调用旧 Web 宿主策略。
  下载仍走 `runv2`/guest decryptor，在线播放仍走分片流接口。
- 原生 renderer、guest/login 实现、runtime 锁文件及配方不变，不需要为本轮整理重建 runtime。
- HTTP 同源/能力鉴权、凭据继承、Wrapper 指纹和启动标记守卫保留。
- CLI 保留上一轮的 SQLite 关闭前连接排空修复；不能将 store.Close 提前到 worker.Wait 之前。
- 不引入 build tags 隐藏安全测试，也不移动用户资料、源码授权材料或已发布附件。

## 仍未拆完

根包依然是 `package main`，`amdl-service.exe` **仍编译 CLI/Web 和旧 Wrapper 兼容代码**。
本阶段是接口和宿主策略分离，不是原生 EXE 已完全剔除旧实现。

`JobAPI.manager` 仍是具体 `*JobManager`：文件删除、下载访问与存储里的私有文件身份直接耦合。
为避免放宽路径/身份校验，本轮不把整个 manager/store 包装成一个大接口，也不迁移 Job 数据模型。
`wrapper_manager.go` 继续拥有子进程、会话收据、状态机、启动标记和安全校验；仅宿主配置与启动策略分出。

下一步应按以下顺序推进：

1. 为文件查询/删除、设置与维护操作分别定义用例接口，并以现有攻击场景测试固定授权边界。
2. 让 `JobAPI` 逐个处理器脱离 store 私有字段，再考虑迁移 HTTP/CLI 到独立包。
3. 明确旧 Wrapper 的 stop/退出所有权后迁移完整进程生命周期；不要让原生 guest 登录复用旧策略。
4. 最后再拆主程序入口和构建依赖，证明原生目标不再编译旧实现后，才能声称完全解耦。

## 验证

新测试使用最小 fake 验证接口、只读 SSE、有序退出和维护模式，且不启动真实 Wrapper 或登录账号。
架构回归限制共享接口包的依赖，并检查原生入口未接入旧宿主。
跨平台 Go race/vet、三目标构建及原生/共享前端回归仍须执行，具体记录见
[本阶段验收](../testing/2026-10-31-phase3-shared-go-interfaces.md)。
