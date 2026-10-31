# JobAPI 与文件服务

以下路径相对 `apple-music-downloader/`。

| 层 | 文件 | 职责 |
| --- | --- | --- |
| HTTP | `jobs_api.go`、`jobs_preferences.go` 中的 JobAPI 方法 | 请求、状态码、SSE、Range 和 ZIP 编码，不访问存储私有身份字段 |
| 用例接口 | `job_api_ports.go` | 任务、文件目录、文件访问、删除、设置、队列状态六组窄接口 |
| 组合层 | `job_api_composition.go` | 保持 NewJobAPI 调用方式，绑定 manager、输出目录、依赖和测试删除钩子 |
| 文件服务 | `job_file_service.go` | 不透明 ID 查询、任务原保存目录、身份校验、打开句柄和整批删除准备 |
| 存储适配 | 组合层中的 `managedFileRepository` | 封装 manager 和根目录查询，私有记录只供文件服务使用 |
| 平台保护 | `secure_read*`、`secure_remove*` | no-follow、句柄、Windows 重解析点、指纹和删除校验 |

`JobAPI` 不持有具体 `*JobManager`。带路径的删除钩子仅由组合层交给文件服务，不暴露给路由。队列状态通过布尔快照读取，不直接访问 executor 或生命周期内部字段。

## 文件访问与句柄所有权

- 文件目录接口只返回公开 JobFile，不暴露相对路径、根目录或身份指纹。
- 文件访问接收不透明 ID，成功返回已校验、已打开的 `io.ReadSeekCloser` 与显示元数据；HTTP 不按路径重新打开，继续通过 `http.ServeContent` 支持 Range。
- 历史任务使用记录中的输出目录；无所属任务的索引文件沿用默认目录规则。
- ZIP 在发送响应头前打开并校验全部输入。准备失败由服务关闭已打开句柄；成功后由 HTTP 关闭整批输入并完成编码。
- 保留查询错误与打开错误的区别，沿用 `not_found`、`no_files`、`file_missing`、`file_changed`、`file_unavailable`，不回显底层路径或错误原文。

## 删除约束

`DeleteJob(ctx, id)` 与 `DeleteRetainingFiles(ctx, id)` 分开。HTTP 不获得 prepare/remover 回调。

删除文件保留原批次根句柄、延迟打开、路径检查和空目录修剪顺序，通过 JobManager.Delete：

1. 检查终态、维护状态与任务预留；输出锁忙时快速返回。
2. 为全部文件准备安全删除操作，全部准备成功后才开始物理删除。
3. 删除成功后移除记录；执行中失败则保留记录以便重试。

这不是文件系统事务，也不新增递归删除。仅删除记录沿用所有权转移事务，不调用文件删除服务；重试、保留文件和跨任务覆盖的锁/身份规则不能放宽。

## 当前边界与验证

用例边界位于主包内部，尚未完成独立 Go 包迁移。Job、JobFile、私有身份类型、repository 和 manager/store 仍在主包，组合层依赖具体实现。维护专属路由和原生任务位置查询等非 JobAPI 路径仍直接使用 manager/store。

后续提取领域模型和错误契约时，不将私有身份字段变为公开 JSON，也不将删除接口退化成接收绝对路径。验证包括契约 fake、关闭句柄、批次删除、Range/ZIP、文件替换、跨任务所有权及平台保护，运行方式见[测试指南](../testing/README.md)。
