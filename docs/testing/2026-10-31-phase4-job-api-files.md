# 第四阶段 JobAPI / 文件用例验收

日期：2026-10-31。基线：`77890cbcc19f9a8842a672010804f29f68fc9ed2`。
本记录保存 `codex/job-api-file-ports` 提交前的本地验收，不作为实时合并或发布状态。
设计与剩余耦合见[当前边界](../development/job-api-file-boundaries.md)。

## 变更与保护范围

- JobAPI 改用六组用例接口，不持有具体 manager，不访问 SQL、私有身份或删除准备回调。
- 具体构造与默认依赖移入组合层；文件打开、历史输出根目录及批次安全删除准备移入文件服务。
- manager/store、平台读取/删除保护、身份算法和持久化格式不变。
- 原有三个底层删除准备攻击测试改为直接调用文件服务，保留原断言、平台条件和攻击步骤。
  原有 HTTP 安全/并发/删除、保留文件与下载目录回归仍执行，没有删除测试或新增平台跳过。
- 64 个原生 UI、下载、分片播放、runv2、登录/guest、运行组件锁与配方文件与基线字节一致。
  `native_download_directory.go` 只移走原 JobAPI 输出根目录方法，原生目录选择和任务位置逻辑不变。

## 本地结果

| 检查 | 结果 |
| --- | --- |
| 新接口与资源所有权 | Range、下载头、ZIP 内容、全部/部分句柄释放、错误状态与脱敏、两种删除接口、历史根目录、无所属任务文件均通过 |
| 默认文件校验 | 无 SQL 替身搭配真实平台 reader，拒绝同路径同长度内容替换 |
| 架构守卫 | 扫描主包所有 JobAPI 方法，以及 HTTP/接口类型，禁止重新访问具体 manager/store、身份类型/字段与底层打开/删除钩子 |
| Windows Go 1.25.0 | `go test -count=1 -race -skip '^TestWrapperSession' ./...`、`go vet ./...` 通过；仅沿用 CI 的 directory-Sync 平台排除 |
| Linux Go 1.25.0 | 禁网容器独立 tmpfs 中全量 `go test -count=1 -race ./...`、`go vet ./...` 通过，无用例排除 |
| 重复验证 | Windows 新接口/文件安全用例、Linux 文件服务/底层准备与替换攻击用例另各执行 25 轮 race，通过 |
| 原生构建 | Windows service/control、Linux guest 均通过；输出到 E 盘本地证据目录 |
| Node 单元 | 194 项通过，包含原生/共享与文档回归 |
| Playwright | 按现有技能流程复用项目测试，设置合成 AAC 夹具，122 项通过，包括真实解码、拖动/切歌和下载独立性 |
| 契约/依赖 | `verify:native`、`verify:boundaries` 通过，无归档 Appliance/WSL 包回流 |
| 文档/格式 | 本地链接和标题锚点检查、`git diff --check` 通过 |

新增 Range 替身测试最初将路由前后 Context 作指针相等比较，未考虑 mux 添加路由变量；
已改为验证请求上下文标记继续传递，未修改生产路由或放宽文件校验。

## 限制

日志、源码测试归档与编译输出位于被忽略的 `.artifacts/phase4-job-api/`，不提交本地产物。
本地验收不访问真实账号、不启动真实 Wrapper、不下载商业媒体，也未生成安装包。
合入与远端 CI 结果以本阶段提交对应的实际 Actions 运行记录为准。
`v2.0.0` 标签、发布附件和用户账号/会话材料未改动。
接口仍位于主包，不能把这一阶段当作全部领域模型、存储和 HTTP 已迁成独立 Go 包。
