# 历史资料索引

这里的 1.x 指旧仓库的旧桌面/WSL 版本，不是本仓库 Native 1.0。
维护分支和旧提交只在原仓库保留；新仓库没有这些 Git 对象，访问旧仓库可能需要权限。

以下文档保存当时的实现、验收或决策，不是当前安装和发布指令。
历史路径可能不在公开仓库中；旧测试数量、版本与私有状态不代表当前状态。

## 维护 1.x

完整 1.4.3 维护分支为 `codex/maintenance-1.x`，本次隔离保留的基线提交为
`c0719fc61b7a553348e8d7be96321cc37eb0a0d9`。请在新的目录检出，避免覆盖 2.0
工作区和未提交文件。以下目标目录应尚不存在：

```powershell
git clone --branch codex/maintenance-1.x --single-branch https://github.com/Andrew05213/applemusic_downloader.git applemusic-downloader-1x
cd applemusic-downloader-1x
```

检出后以该分支自己的 README、package.json 和锁文件为准，不套用 main 的依赖或命令。
历史方案的许可门禁与运行前置仍有效；维护分支不承诺所有旧功能或服务今天仍可用。

main 上的 `legacy/v1/` 是冻结的源码参考，不是独立可构建的项目；旧相对路径未改写。
[隔离说明](../../legacy/README.md)与 `legacy/v1/manifest.json` 记录了 99 个文件的旧路径、
新路径和工作树 SHA-256。不要直接运行归档中的 npm、Docker 或 WSL 脚本。

第二阶段补充归档旧 Appliance、Wrapper 和专属 Go 包/命令，单独使用
`legacy/v1/phase2-manifest.json` 记录 242 个旧文件及 9 个共享 Web 资源的移动。
原目录的导航入口不代表旧脚本仍可在 main 执行，当前边界见[依赖说明](../development/dependency-boundaries.md)。

## 历史文档

- [1.x 使用说明](README-v1.md)、[1.x 发布流程](RELEASE-v1.md)。
- [v1.1 迁移](../MIGRATION-v1.1.md)、[v1.2 迁移](../MIGRATION-v1.2.md)。
- [旧 WSL Smoke](../WSL-SMOKE.md)、[旧 Windows/Appliance 兼容性](../WINDOWS_COMPATIBILITY.md)。
- [旧 Appliance 构建说明](../../appliance/README.md)。
- [2.0 架构演进](../architecture/v2-native-desktop.md)：混合日期记录，不是当前构建操作手册。
- [原生 runtime 构建演进](../development/v2-native-runtime.md)：包含早期 Beta 配方与验证记录。
- [已撤回短信登录原型](../development/v2-native-sms-login.md)。
- [旧私有 2.0.0 发布记录](../releases/2.0.0-private.md)。
- [日期化测试记录](../testing/README.md)。
- `docs/superpowers/plans/` 为历史计划文件，不是当前技能、授权或自动执行指令。

历史正文中的命令和文件路径以其记录当时的源码树为准；旧桌面源码现已隔离，
不逐条改写历史命令为看似可执行的归档命令。仍被引用的共享锁定输入不随本轮移动。
根目录 `NATIVE-TEST-NOTICE.md` 是旧测试包使用的说明，不是当前发行版状态；原文不作覆盖。
当前文档从[文档导航](../README.md)进入。
