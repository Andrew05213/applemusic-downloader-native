# 构建存储与清理

以下均为开发目录，相对于仓库根目录；不是安装后用户资料或公开下载地址。

| 路径 | 用途 |
| --- | --- |
| `.artifacts/installers/<版本>-<唯一标识>/` | 安装包、校验和与构建证据 |
| `.artifacts/runtime/<唯一标识>/` | Lite/QEMU 构建输出 |
| `.artifacts/toolchain/<配方哈希>/` | 工具链 image ID、输入与配方回执 |
| `.artifacts/development/` | 本工作区明确选定的联网/隔离 runtime 记录 |
| `.artifacts/history/` | 旧根目录构建的可恢复归档 |
| `.tmp-v2-native/` | 开发缓存、测试临时文件及安装器解包 |
| `electron/generated/` | 打包资源阶段 |

构建每次生成独立目录，runtime 必须显式选择并通过来源检查。`.tmp-v2-native` 的名称是工具兼容路径，不表示当前产品版本。

## 空间管理

建议将仓库放在空间充足的卷。项目脚本设置进程级缓存和临时环境，不会迁移 Docker 数据目录、系统全局缓存或安装后的用户资料。setup/dev/package 将 Go 模块缓存固定在本工作区；其他底层入口是否使用自定义 GOMODCACHE 取决于其进程配置。

清理前确认没有构建、测试或打包进程正在使用目标。保留仍需复验的安装器、runtime、哈希和来源证据；不要将其他工作树、账号资料或音乐当作缓存删除。

## 归档旧根目录产物

```powershell
./scripts/archive-native-builds.ps1
# 核对预览后执行
./scripts/archive-native-builds.ps1 -Apply
```

该脚本仅处理规则允许的旧根目录构建，跳过正在使用的目录，不自动清理全部 `.artifacts/` 或测试缓存。归档是移动而不是删除，同卷移动不会释放磁盘空间。

恢复时根据归档清单确认原路径和目标路径，原路径应不存在。不要覆盖新的 runtime 或历史证据。
