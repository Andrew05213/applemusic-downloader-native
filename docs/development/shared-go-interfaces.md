# 共享 Go 接口与宿主适配

以下路径相对 `apple-music-downloader/`。

| 层 | 路径 | 职责 |
| --- | --- | --- |
| 应用接口 | `internal/service/ports.go` | 执行、查询、提交、取消、事件读取及启动/等待；不依赖 HTTP、SQL、文件或进程实现 |
| 有序退出 | `internal/service/lifecycle.go` | 取消 worker → 独立上下文等待退出 → 关闭存储，保留错误链 |
| 类型绑定 | `service_ports.go` | 用现有 Job/事件类型实例化接口，编译期检查实现 |
| CLI | `cli_runtime.go` | 等待依赖查询接口，取消依赖取消接口 |
| 旧 Web HTTP | `legacy_web_http.go` | 提交、只读 SSE、Wrapper 状态/验证码分别依赖窄接口，保留响应契约 |
| Wrapper 启动策略 | `internal/legacyweb/wrapper.go` | 决定是否启动，维护模式短路凭据检查，缺少凭据不启动 |
| Wrapper 宿主 | `legacy_wrapper_host.go` | 环境/继承凭据优先级、secret-fd、loopback、会话与脱敏配置 |
| 装配 | `server.go`、`service_lifecycle.go` | 绑定路由、鉴权、持久化及具体实现，处理 typed-nil |

接口使用类型参数保留领域类型，不通过 `any` 或 JSON 绕过检查。`JobExecutor` 是现有签名的实例化别名；只读 SSE 不拥有提交和取消任务的能力。

## 退出和安全边界

存储关闭必须晚于 worker 退出，不能将 `store.Close` 提前到 `worker.Wait` 之前。关闭过程使用独立上下文等待，避免启动上下文取消导致清理立即失效。

原生桌面、下载和播放不调用旧 Web 宿主策略。HTTP 同源/能力鉴权、凭据继承、Wrapper 指纹与启动标记检查继续有效。接口重构不改变 API/SQLite 格式、安装身份或账号目录。

## 当前耦合

根包仍编译 CLI/Web 和旧 Wrapper 兼容代码。`wrapper_manager.go` 拥有进程、会话收据、状态机、启动标记和安全校验；这里只分离宿主配置和启动策略，尚未迁移整个生命周期。

JobAPI 已消费用例接口，当前关系见[JobAPI 与文件服务](job-api-file-boundaries.md)。领域模型、manager/store 及组合层仍位于主包。继续拆包前，应固定稳定错误契约及 stop/退出所有权，不把具体 store 包装成一个无边界的大接口。

## 验证

接口和生命周期测试使用最小 fake，验证只读 SSE、有序退出、维护模式及错误链，不启动真实 Wrapper 或登录账号。依赖回归限制共享接口包的依赖，并检查原生入口不接入旧宿主。运行方式见[测试指南](../testing/README.md)。
