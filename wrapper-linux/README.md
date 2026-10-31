# Wrapper Linux 参考资料

旧 Wrapper 源码、Makefile、预编译程序和 rootfs 参考输入保存在 `legacy/v1/wrapper-linux/`。它们不是当前 Native runtime 的构建输入，不要直接执行归档程序或用旧 rootfs 替代 runtime。

当前 runtime 使用 `native-runtime/inputs.lock.json` 锁定的 Lite 归档和工具链镜像。归档身份见[清单](../legacy/v1/phase2-manifest.json)，实际依赖见[依赖边界](../docs/development/dependency-boundaries.md)，构建前置见[开发指南](../docs/development/README.md)。
