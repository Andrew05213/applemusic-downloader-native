# 旧版隔离边界

完整 1.4.3 维护基线保存在 `codex/maintenance-1.x`，提交
`c0719fc61b7a553348e8d7be96321cc37eb0a0d9`。这是旧版维护入口，不是把当前
2.0 的工作树切换到旧版；请在单独 checkout 中维护，不覆盖未提交文件。

`legacy/v1/`（相对仓库根目录）保存从 2.0 开发树隔离出来的旧桌面、WSL/DRM 工具、配置和专属测试，
原有相对目录结构及文件字节保留。它不是可在当前根目录运行的完整 1.x 产品，
也不保证隔离目录中的旧相对路径仍可执行。不要直接调用归档脚本；运行 1.x 应使用维护分支。

当前根 package.json 不再暴露 WSL/Appliance 构建命令；旧测试不进入 2.0 测试集合。
兼容的 dist:win 别名仍只转到原生 dist:native，不是旧 WSL 安装器。

第一阶段 99 文件清单保留在 `v1/manifest.json`，不改写原始隔离证据。
第二阶段另将 242 个旧 Appliance/Wrapper 文件、专属 Go 包和命令归档，
9 个共享 Web 资源独立到 `web/`；逐文件哈希见 `v1/phase2-manifest.json`。
旧目录两份 README 留作导航入口，其原文保存在归档中。

`appliance/release-policy.json` 仍被原生打包和校验使用，保留原路径和关闭状态。
原生 runtime 的配方、锁文件及 guest 源码未改动；Go 根包尚有 CLI/Web 兼容代码，
不能按名称批量删除。当前依赖及下一步边界见[依赖说明](../docs/development/dependency-boundaries.md)。

完整旧版的独立检出方式见[1.x 维护入口](../docs/legacy/README.md#维护-1x)。
历史文档中的命令、路径和测试数量仅描述当时的源码树，不作为 main 的操作步骤。
当前指南从[文档导航](../docs/README.md)进入。已发布 v2.0.0 标签不随此结构调整移动。
