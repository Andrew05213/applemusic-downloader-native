# Wrapper Linux 历史入口

旧 Wrapper 源码、Makefile、预编译程序和 rootfs 参考输入已原样归档到
`legacy/v1/wrapper-linux/`，不是当前 2.0 runtime 的构建输入；
[第二阶段清单](../legacy/v1/phase2-manifest.json)记录移动前的路径和 SHA-256。
忽略的本地数据不随本轮移动或清理。

2.0 使用 `native-runtime/inputs.lock.json` 锁定的 Lite 归档和工具链镜像；
该镜像的历史来源仍需保留，但不读取这个目录生成新 guest。
不得用旧 rootfs 替代联网/隔离 runtime，也不直接执行归档程序。

完整 1.x 维护见[独立检出说明](../docs/legacy/README.md#维护-1x)，
当前入口见[依赖边界](../docs/development/dependency-boundaries.md)。
