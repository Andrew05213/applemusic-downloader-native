# WSL2 Appliance 真实 Smoke（开发者）

> 历史记录：以下内容保留当时的实现、命令与验收语境，不代表当前发布状态。当前指南见[文档导航](README.md)，不要直接用本页旧命令安装或发布 2.0。
> 下文 npm 命令已从 main 移除；它们只按历史源码树解释。需要旧版环境请先[独立检出 1.x](legacy/README.md#维护-1x)，不要在 `legacy/v1/` 归档内运行。

此入口只面向本地开发和发布前验证。它复用 Electron 的 `WslManager`，不会读取
`config.yaml`，不会调用 Docker，也没有 `unregister` 或 `wsl --shutdown` 代码路径。
只有下文显式的旧 PoC bootstrap 会调用一次受审计的 `update-stdin`；其他模式仍禁止
更新。结果只输出白名单字段；不会输出 owner nonce、Windows 用户路径、日志、命令
参数或环境变量值。

## 默认安全检查

```powershell
npm run smoke:wsl
```

检查顺序固定为：

1. 确认只存在名为 `AppleMusicDownloader` 的 WSL2 发行版。
2. 同时核对 host `ownership.id`、发行版内 `0600` marker 与
   `service/runtime/version`。
3. 通过 `WslManager.probe()` 和 `status()` 核对 WSL 与 supervisor。
4. 验证已登记下载目录的 Windows 最终身份及 `wslpath` 转换；路径必须同时包含
   中文和空格。
5. 仅当后端本来就在运行时，检查本机代理健康身份和活动任务。

任一所有权条件不满足都会在第 2 步前后 fail closed，不会继续访问路径、健康接口
或生命周期操作。

## 最终产物绑定的无凭据网络预检

完成最终 rootfs/runtime 构建后，发布验证必须显式绑定同一版本的受控 dist manifest：

```powershell
$version = node -p "require('./package.json').version"
$manifest = Resolve-Path "appliance\dist\applemusic-downloader-wsl-manifest-$version-amd64.json"
npm run smoke:wsl -- --manifest $manifest
```

此模式在执行任何 WSL 命令前，要求 manifest 路径精确等于当前 `appliance/dist` 的
版本化 `local-poc` manifest，并校验 rootfs 与 runtime 的大小和 SHA-256。随后它复核
host state、ownership、supervisor 和完整 build identity；服务必须已经处于 `stopped`。
通过后才调用现有的 `network-prepare-stdin` 与 `network-preflight`，使用最终 Windows
helper 和同一 Android chroot/Bionic Bag 路径确认 `ready/complete/reachable`。

该预检不调用 `credential-stdin`，不读取 `config.yaml`、DPAPI 凭据、token、cookie 或
2FA，不启动 backend、不创建任务、不下载媒体、不更新 runtime，也不调用 terminate、
unregister 或全局 shutdown。输出仅保留版本、build ID、摘要和固定网络状态；Windows
用户路径、代理地址、stderr 与控制细节不会输出。服务运行中或任一最终身份不匹配时会
在准备 bridge 前 fail closed。

## v1.3.8 network-only TLS 验收

`1.3.8` 的 CA/TLS 验收必须使用独立模式，且只可针对全新安装、已经停止并精确匹配
最终 manifest 的 Appliance：

```powershell
$version = node -p "require('./package.json').version"
$manifest = Resolve-Path "appliance\dist\applemusic-downloader-wsl-manifest-$version-amd64.json"
npm run smoke:wsl -- --network-only --manifest $manifest
```

此模式先校验 manifest/rootfs/runtime 和持久 runtime identity，再执行 ownership、
stopped status、Windows bridge prepare 与 Android chroot/Bionic Bag preflight，最后以
受审计的 stopped cleanup 回收本轮 bridge。它不调用下载路径、health、任务、configure、
backend start、`credential-stdin`、`update-stdin`、terminate、unregister 或全局 shutdown；
更重要的是，它不会检查、打开、读取或交付 `credentials.bin`，即使该文件存在。

目标版本与已安装版本不一致、服务非 stopped、manifest/CA/build identity 不匹配，或
network status 不是 schema v2 的 `ready/complete`，都会在 native preflight 前 fail closed。
当前机器仍安装旧版本时，该失败是预期版本门，不得把它解释为允许自动迁移或删除旧
发行版。该验收仅证明网络桥、TLS trust、Bag 解析和 key lookup；不证明登录、媒体下载、
历史认证崩溃或 post-auth 路径。

`1.3.8` 的 CA/TLS 验收只接受已安装且完全
匹配同一 `1.3.8` manifest 的 runtime；跨版本请求必须在触碰 proxy、control、artifact 或
credential provider 前返回 `runtime_reinstall_required`。历史 `1.2.13 -> 1.2.14` 的
WSLInterop trampoline 已永久禁用：旧 supervisor 会在校验后按路径重新打开 helper，
而每用户安装目录不能把该后续打开操作绑定到已校验字节，存在路径 TOCTOU。保留的
descriptor/bootstrap 仅用于构建身份兼容性校验，绝不自动执行或提交给 supervisor。

## 私有 runtime repair 检查

repair 入口必须显式指定受控的 `local-poc` manifest。默认命令只做 preflight，
不会停止发行版或提交更新：

```powershell
$manifest = Resolve-Path appliance\dist\applemusic-downloader-wsl-manifest-1.3.8-amd64.json
npm run smoke:wsl:repair -- --manifest $manifest
```

只有负责人确认目标 artifact、无凭据且活动任务为 0 后，才可对私有测试 Appliance
执行普通原地 repair：

```powershell
npm run smoke:wsl:repair -- --manifest $manifest --execute-private-runtime
```

普通路径调用 `WslManager.repair()`，禁止 `terminate`、`unregister` 和
`wsl --shutdown`。若早期 PoC 的 Wrapper 留下无法在运行中卸载的 proc/urandom
mount，必须另外显式选择 legacy quiesce 路径：

```powershell
npm run smoke:wsl:repair -- --manifest $manifest --execute-private-runtime --quiesce-legacy-mounts
```

此例外路径只允许专用发行版最多一次 `terminate` 和一次 `update-stdin`，并在启动、
停止、terminate、清理 mount 和提交更新前重复验证 migration lock 与发行版 ownership。
它把 recovery marker 持久化为 `prepared → terminating → quiesced → target_verified`；
旧版留下的 `submitted` marker 仍可安全读取。marker 同时绑定 owner nonce、
source/target manifest、runtime SHA-256 和新事务 ID。

如果进程在 `terminating` 后恢复，只继续清理而不会再次 terminate；如果在
update 前失败且尚无 durable `updateTransaction`，同一事务 ID 可以安全继续；一旦
`updateTransaction` 已落盘，它就是是否可能触达 `update-stdin` 的权威边界，恢复只做
精确协调，绝不盲目重发。只有 host state、当前 release、supervisor 身份和目标事务
全部一致时，才进入 `target_verified`。

`target_verified` 是持久终态回执，不再自动清除；成功响应丢失或机器重启后，同一
manifest 只返回 recovered，不会再次 terminate/update。显式选择不同的已校验 manifest
时，必须先重新证明旧终态、无凭据和队列为空，才能用新事务替换旧回执。未知 phase、
损坏 marker、未决/不匹配 transaction 或无法证明的回滚状态都会 fail closed；不要
手工删除 marker、重复更新或 unregister 发行版。

## 旧本地 PoC 的一次性 runtime 身份迁移

早期本地 PoC 的 supervisor 不认识 `.amdl-release.json`，不能直接补写身份后接管。
开发者必须显式提供旧 runtime artifact、当前源码生成的新 manifest 和新 runtime：

```powershell
npm run smoke:wsl:bootstrap-local-poc-identity -- `
  --legacy-runtime "C:\path\old-runtime.tar.gz" `
  --target-manifest "C:\path\new-manifest.json" `
  --target-runtime "C:\path\new-runtime.tar.gz"
```

此入口默认关闭，并且只接受 `channel=local-poc`、同版本、未公开发布的严格 manifest。
它不是任意同名发行版的修复或认证工具，威胁模型仅覆盖由本仓库早期构建生成、但缺少
runtime identity 的已知本地 PoC；候选发行版自身的 Linux 工具不构成独立信任根。

迁移顺序固定为：

1. 旧 artifact 的 size/SHA 必须精确等于持久 state manifest，并用 `tar --compare`
   证明 current release 内容一致；仅允许 mode/mtime 差异。archive/current 成员集合也
   必须一致，只排除 `rootfs/data/**`、proc/dev 运行时挂载内容及待创建 metadata。
2. 再次证明无凭据、无活动任务、无 install/update/pending transaction，停止后端并
   terminate 一次专用发行版以清除旧 Wrapper 泄漏挂载；不调用全局 `wsl --shutdown`。
3. 新 bundle 在 Windows 和 WSL 两侧校验后，通过 stdin 复制为 ext4 上的 root `0600`
   snapshot；该 inode 以只读 bind mount 映射到受控 DrvFS placeholder。旧 updater 只
   收到 placeholder 路径，不能在 hash 与解包之间读取被 Windows 改写的原文件。
4. 写入 host recovery marker 后只调用一次旧 `update-stdin`。响应丢失时绝不重复更新；
   只有 current 内容、update marker、目标 executable、新 supervisor 与健康接口全部
   证明已提交，才继续。无法证明时保留 recovery marker 并 fail closed。
5. 新 supervisor 生效后，才用 hard-link no-replace 发布精确的 `.amdl-release.json`，
   核对 status 上报相同 runtime SHA/transaction，随后持久化新 manifest/installed 字段
   并进入下述 ownership 接管。失败回滚只删除本次发布且字节完全一致的 metadata。

bridge mount、ext4 snapshot 与 Windows placeholder 只在事务达到可证明终态后清理；
清理失败会阻止 metadata 发布和接管。若更新结果无法证明，流程不会 terminate WSL、
不会重发更新，也不会清理仍可能被旧 supervisor 使用的只读 bridge；它保留 recovery
marker 并返回 `bootstrap_update_recovery`。下一次运行只能重新证明现有 target/rollback
状态，绝不会再次发送旧 `update-stdin`。命令可输出 runtime digest 和 transaction ID，
但不输出路径、owner 或秘密。普通 smoke、CI 和本轮开发验证不得执行此真实迁移命令。

## 旧本地 PoC 的一次性接管门

接管默认关闭，只能由开发者显式运行：

```powershell
npm run smoke:wsl:adopt-local-poc
```

此命令不是通用“自动接管”。只有以下条件全部成立时才会使用 no-replace 发布写入
host owner、发行版 marker 和 state ownership：

- host、state 和发行版中都没有既有 owner；
- Windows Lxss 注册的唯一同名发行版为 WSL2，且 BasePath 最终路径精确等于
  `%LOCALAPPDATA%\AppleMusicDownloader\wsl`；
- 持久 manifest、installed version、runtime SHA-256、runtime transaction ID 与
  `probe/status` 完整一致；
- `credentialsConfigured=false`，桌面 DPAPI 凭据文件不存在；
- supervisor 正在运行、活动任务为 0，install/update/pending transaction 全为空；
- 服务身份为 `applemusic-downloader / wsl-appliance`。

兼容早期本地 PoC：若 state 只缺 `installedVersion`、`installedRuntimeSha256`
或 `installedRuntimeTransactionId`，接管门可从已经完成上述核验的 supervisor
status 派生缺失值，并先通过 state 的临时文件 + 原子替换持久化。已存在的字段必须
精确匹配，不能用 status 覆盖；manifest 缺失、manifest/runtime digest 不一致、
transaction ID 非 64 位十六进制，或任一 install/update/pending transaction 存在时
仍会 fail closed。ownership 后续提交失败时，本次派生字段也随接管事务回滚。

提交前会再次核对全部外部身份。host 文件和 WSL marker 均采用同卷 hard-link
no-replace 发布；后续步骤失败时只回滚本次生成且内容仍匹配的 marker。owner 值不
进入 stdout、argv 或环境变量。

缺少 release metadata 的旧 PoC 必须先完成上一节 bootstrap；接管命令本身不接收
artifact，也不会更新。只有负责人明确批准本机 PoC 接管后，才可运行一次性命令。

## 生命周期检查

确认已经完成所有权绑定、没有真实凭据且没有活动任务后运行：

```powershell
npm run smoke:wsl:lifecycle
```

该模式验证 start、显式 stop、独立 manager 进程退出后后端继续运行、再次 stop 和
restart，并在最后恢复进入 smoke 前的运行/停止状态。每次 stop 前都会重新查询活动
任务；发现任务或 `credentials.bin` 时立即拒绝。启动过程会触发 Appliance 现有的
Windows 下载目录原子探针（临时写入、fsync、同目录 rename、清理）。

每次后端停止后还会用固定参数运行 `findmnt`，确认当前 release（以及 `current`
别名）下 `rootfs/proc`、`rootfs/dev/urandom` 及其子路径的挂载层数均为 0。只忽略
WSL 已确认的中英文 localhost 代理整行警告；其他 stderr、无效 JSON 或任何残留层
都会 fail closed。此检查覆盖 Wrapper 正常运行、启动失败或重启退避后由后端停止
路径执行的公共清理逻辑。

## 凭据 sentinel 审计（仅限更新后的私有 runtime）

此模式默认拒绝执行。完成 runtime 重建和原地更新后，由发布负责人显式运行：

```powershell
npm run smoke:wsl:credentials -- --execute-private-runtime
```

执行前会再次核对 host/distro ownership、目标 manifest 的版本、runtime SHA-256、
runtime transaction ID、`credentials.bin` 不存在以及活动任务数为 0。任一条件不满足
时不会调用 `credential-stdin`。

测试凭据由 Node 在内存中随机生成，只通过 `WslManager` 的 `credential-stdin` 送入
Appliance。审计覆盖 Windows 子进程 argv/env、WSL supervisor/backend/Wrapper 的
`/proc/<pid>/cmdline` 与 `environ`、最近 200 行 journal 和 diagnostics、
`jobs.db`/WAL/SHM、API 错误与可安全订阅的终态任务 SSE，以及现有 rootfs、runtime、
installer 产物。扫描模式不会作为 argv、环境变量或临时文件传递。

无论 live 审计成功还是失败，清理阶段都会尝试 graceful stop、`wsl --terminate`
专用发行版，再以无凭据状态启动并确认 `credentialsConfigured=false`。停止、终止、
重启、持久化复扫或凭据文件复查任一步骤失败都会 fail closed。此模式不读取
`config.yaml`、不创建 DPAPI 凭据文件、不创建任务、不下载媒体、不更新 runtime，
也不执行 `unregister` 或 `wsl --shutdown`。stdout 只包含通过/失败和脱敏位置。
