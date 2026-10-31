# 第四阶段：JobAPI 与文件用例边界

基线为 `77890cbcc19f9a8842a672010804f29f68fc9ed2`。
本阶段隔离 HTTP、存储和文件身份操作，不改变接口路径、JSON/SQLite 格式或原生媒体实现。

## 当前分层

以下路径相对 `apple-music-downloader/`：

| 层 | 文件 | 职责 |
| --- | --- | --- |
| HTTP | `jobs_api.go`、`jobs_preferences.go` 中的 JobAPI 方法 | 解析请求、状态码、SSE、范围响应和 ZIP 编码；不访问 manager/store/身份字段 |
| 用例接口 | `job_api_ports.go` | 任务、文件目录、文件访问、删除、设置、队列状态六组接口，不用一个大接口包装整个 manager |
| 组合层 | `job_api_composition.go` | 保持 `NewJobAPI` 调用方式；绑定具体 manager、输出目录、默认依赖和测试删除钩子 |
| 文件服务 | `job_file_service.go` | 用不透明 ID 查询记录、选择任务原保存目录、校验身份并打开句柄、准备整批删除 |
| 存储适配 | 同组合层中的 `managedFileRepository` | 封装 manager 和输出根目录查询；私有存储记录仅供文件服务使用 |
| 平台保护 | `secure_read*`、`secure_remove*` | 原有 no-follow、句柄、Windows 重解析点、指纹及删除校验实现，不因本轮搬迁而改变 |

`JobAPI` 不再持有具体 `*JobManager`。HTTP 环境配置中也不保留 `RemoveFile`；
带路径的删除钩子只在组合层传给文件服务，不暴露给路由处理器。
队列能力通过只包含布尔状态的快照读取，不直接查看 executor 或生命周期错误字段。

## 文件访问与句柄所有权

- 文件目录接口只返回原有公开 `JobFile`，没有相对路径、根目录或身份指纹。
- 文件访问接口只接收不透明 ID。成功返回已校验、已打开的 `io.ReadSeekCloser` 和显示元数据，
  HTTP 不重新按路径打开文件，仍由 `http.ServeContent` 支持 Range。
- 文件服务为历史任务使用记录中的输出目录；无所属任务的已索引文件仍使用原有默认目录规则。
- ZIP 在发送响应头前打开并校验全部输入。任一步失败，服务关闭先前打开的句柄；
  成功后由 HTTP 关闭整批输入。ZIP 编码本身仍由 HTTP 完成。
- 保留元数据查询错误和实际打开错误的区别，沿用 `not_found`、`no_files`、
  `file_missing`、`file_changed`、`file_unavailable` 等响应，不向客户端回显底层路径或错误原文。

## 删除不变式

`DeleteJob(ctx, id)` 与 `DeleteRetainingFiles(ctx, id)` 分开，HTTP 不获得底层 prepare/remover 回调。
文件服务内保留原批次根句柄、延迟打开、路径检查和空目录修剪顺序。
真正的预校验、逐项删除和记录删除仍通过 `JobManager.Delete` 的原实现执行：

1. 检查终态、维护状态和任务预留；输出锁忙时仍快速返回。
2. 对所有文件准备安全删除操作，全部准备成功后才开始物理删除。
3. 执行删除并成功后移除记录；运行中删除失败仍保留记录，允许后续重试。

这不是文件系统事务，也不新增递归删除。仅删除记录仍走原有所有权转移事务，
不调用文件打开/删除服务；重试、保留文件与跨任务覆盖的锁和身份规则没有放宽。

## 剩余工作

本轮建立的是**主包内部**的用例边界，不是已完成 Go 包迁移。
`Job`、`JobFile`、私有身份类型、repository 和 manager/store 仍在 `package main`；
组合层也仍需要具体实现。设置用例与 HTTP 方法暂时同文件，后续可以按职责搬迁。
维护专属路由、原生任务位置查询等非 JobAPI 路径仍直接使用 manager/store，本轮不扩散修改。
旧 CLI/Web、Wrapper 兼容代码仍会参与根包编译。

后续应先抽取领域模型和稳定错误契约，再逐个移动用例与 HTTP 包；
不要把私有身份字段改成公开 JSON，也不要将删除接口退化成传入绝对路径。

测试和未验证范围见[本阶段验收](../testing/2026-10-31-phase4-job-api-files.md)。
