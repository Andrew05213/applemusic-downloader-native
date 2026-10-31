# Appliance 历史入口

旧 WSL Appliance 的配方、锁文件、补丁、原生探针、测试和 overlay 已归档到
`legacy/v1/appliance/`，只供查阅；原始内容及哈希见
[第二阶段清单](../legacy/v1/phase2-manifest.json)。不要直接运行归档脚本。

本目录仅保留 `release-policy.json`：当前原生安装器仍携带这份关闭的旧版策略，
原生公开构建权限由 `native-runtime/release-policy.json` 独立决定。不能删除、合并或打开旧策略来规避检查。

需要完整旧版请[独立检出 1.x](../docs/legacy/README.md#维护-1x)。
当前 2.0 的入口和剩余共享边界见[依赖说明](../docs/development/dependency-boundaries.md)。
