# 旧源码参考边界

`legacy/v1/` 保存前身项目的旧桌面、WSL/DRM 工具、配置和专属测试，不是本仓库 Native 1.0，也不是独立可构建的产品。原始字节和相对结构用于来源核对，不保证旧脚本仍可运行。

- `v1/manifest.json` 记录桌面归档的基线、路径和哈希。
- `v1/phase2-manifest.json` 记录 Appliance/Wrapper 归档及共享 Web 资源的路径和哈希。

不要直接执行归档脚本。当前测试不扫描归档的专属测试，当前安装器不携带旧桌面、WSL 或 Web 壳。

部分兼容回归仍读取归档锁文件。`appliance/release-policy.json` 也仍被原生打包和校验使用，不能根据名称直接删除。Go 根包仍含 CLI/Web 兼容代码，见[依赖说明](../docs/development/dependency-boundaries.md)。

原始许可证、清单和[历史 NOTICE](../docs/legacy/NOTICE-v1)保留不变。更多背景见[归档文档](../docs/legacy/README.md)；当前开发从[开发指南](../docs/development/README.md)开始。
