# Native 1.0.1 冻结材料记录

本目录只记录公开安装包的实际身份和材料状态，不修改旧版证据，也不代表对应源码或授权审查已完成。

- 发布提交：`ad21b378334e32b15016107ca506ac8850471835`。
- 安装器：`AppleMusicDownloader-1.0.1-Setup.exe`，252,216,880 字节。
- SHA-256：`f6088bd70d3d482ef0a45552a19af5a332a6f2c72cc000ae06d817dc08c2ab12`。
- [公开 Release](https://github.com/Andrew05213/applemusic-downloader-native/releases/tag/v1.0.1)与[对应发布工作流](https://github.com/Andrew05213/applemusic-downloader-native/actions/runs/41439352929)。
- [冻结清单](inventory.json)记录发布附件、286 个实际负载文件、32 个本轮范围内的 guest 文件、Go build info、Debian 二进制来源和历史构建文件哈希。
- [逐项材料矩阵](materials.csv)分别记录身份、源码、构建材料和许可/授权状态；不是已通过清单。

后续 main、CI 和工具更新不改变上述产物。新生成的候选资料只存放 `.artifacts/`，不自动提交或发布。

## 本轮范围

QEMU 及 DLL/固件、Debian 内核/BusyBox 及静态链接材料、实际 Go/npm 范围、构建来源和声明记录。本轮按维护者要求暂不继续核查 Android/Apple rootfs：保留未完成状态，不将搁置解释为通过。

README 仅列两个直接上游的署名。原下载器整体许可证仍待确认，不能把某个子目录的许可扩展到整个下载器。Wrapper Lite 固定提交的 MIT 文本已核实；其他组件清单保留在核查资料中。

## 已建立的对应关系与限制

| 对象 | 实际证据 | 仍缺少的材料 |
| --- | --- | --- |
| QEMU | 183 项与锁定 SHA-512 的原安装器逐文件一致：1 EXE、114 DLL、68 share 文件 | 精确对应源码、补丁、MSYS2 包与配置、固件构建配方、逐文件声明 |
| Linux | vmlinuz 和 14 个模块与指定 Debian `.deb` 字节一致；其 Source 为 `linux-signed-amd64 (6.1.177+1)`，Built-Using 为 `linux (= 6.1.177-1)` | signed 源包/构建流程、签名链、最终配置与构建证据及交付核查 |
| BusyBox | 实际二进制与 `1:1.35.0-4+deb12u1+b1` amd64 包一致；Source 为 `busybox (1:1.35.0-4+deb12u1)` | `Built-Using: glibc (= 2.36-9+deb12u14)` 的源码与适用静态重链接材料、+b1 构建记录 |
| 已发布 Debian 附件 | 四份归档通过 `.dsc` 大小和 SHA-256 对照，含 Debian rules 和配置片段 | 旧附件中 Dockerfile/inputs.lock.json 仍是旧 harness 配方；不能冒充 1.0.1 全套构建输入；未含 glibc/signed 包装 |
| Go | service 的 32 模块、desktop 的 x/sys、guest 的无第三方模块记录已固定；32 模块的源码归档 h1 已对照 | 已发布安装器没有专门 Go 声明目录；需生产文件/版权/NOTICE 与 Go 1.25.0 标准库声明审核 |
| npm/Electron | app.asar 20 文件、default_app.asar 13 文件，均无独立 node_modules；Electron 45.0.0 的许可证和 Chromium 声明随包存在 | 不据此排除 Electron 内嵌代码；NSIS 外层及 elevate.exe 仍需单列来源/声明 |

本次 `.deb` 是从 Debian 官方 HTTPS 地址取得并按哈希固定；未完成原快照签名、`.dsc` 签名和独立重建。相同版本号不替代二进制身份比较，源码候选不等于已经向旧版用户交付。

## 只读清点

使用从上述安装器提取的精确负载目录；目录中的额外文件也会拒绝。工具不运行安装器或应用，不下载二进制，也不修改输入。

```powershell
node native-runtime/release-materials.js inventory docs/compliance/releases/1.0.1/inventory.json <旧安装器> <解包负载目录> .artifacts/compliance/1.0.1-inventory.json
```

输出包含独立 ASAR 清单和解包后的 guest 文件哈希。`system/` 只记录搁置数量，不展开其授权核查。`reviewComplete=false` 和 `incomplete=true` 始终保留。

## Debian 候选资料闭包

在有 `dpkg-deb` 的环境中，以冻结清单记录的两个实际二进制包生成计划。包哈希、架构、版本、Source/Built-Using 都须匹配；文件名或同版本其他包不能代替。

```sh
node native-runtime/release-materials.js debian-plan docs/compliance/releases/1.0.1/inventory.json .artifacts/compliance/1.0.1-debian-plan.json <busybox-static.deb> <linux-image.deb>
```

计划必须得到四个源包：busybox、glibc、linux-signed-amd64、linux。它证明元数据闭包，不证明这些材料已收齐或重链接要求已满足。JSON 保留二进制证据，TSV 用于收集器。

在锁定构建镜像内，将 JSON 计划的同名 `.tsv` 只读挂载到 `/in/source-plan.tsv`，新空目录挂载到 `/out`：

```sh
sh native-runtime/collect-debian-sources.sh /in/source-plan.tsv
```

APT 仍使用 `20260728T000000Z` 快照并验证索引；脚本保留索引，校验 `.dsc` 的归档哈希。无法取得的精确源包保持失败，不自动改版本或换最新包。`.dsc` 签名独立验证、配置、构建和静态重链接材料仍须补齐。

取得候选材料后，必须选用发布提交的独立源码目录进行打包：

```sh
node native-runtime/package-debian-sources.js <收集目录> .artifacts/compliance/1.0.1-debian-candidates.zip docs/compliance/releases/1.0.1/inventory.json <发布提交源码目录>
```

打包器校验完整计划、源包身份/归档大小/哈希、全部历史构建文件哈希；不再静默复制当前 main 的配方。输出仍是部分候选包；不含 QEMU，也不意味着已完成签名、重建、授权、静态链接或公开交付审核。

## QEMU 与 Go 工具

- `inventory-qemu.js` 保留 manifest 和构建来源检查，新增逐 DLL/固件的材料状态；所有已分发文件都纳入，不按“未运行”跳过。
- `collect-go-licenses.js` 从实际二进制记录主模块及依赖。执行工具链的 `GOVERSION` 必须与所有选定二进制匹配，防止把当前 Go 的许可证用于旧二进制。替换模块须另审；候选目录不等于生产链接范围，也不会自动进入旧安装包。

## 验收与后续

1. 文件身份匹配、源码可得、构建材料完整、授权/声明确认分别记录。
2. QEMU 精确源码、DLL/固件许可映射未取得时保持阻塞；不能以同版本官方源码补位。
3. Debian 必须覆盖实际 Source/Built-Using，并另审最终配置、签名包装及 glibc 的适用重链接材料。
4. 候选资料经过人工审核后，才可决定是否为旧安装器新增独立补充材料。公开候选包、修改 Release 或发布新版本均需维护者另行批准。
5. Android/Apple 搁置项不计入通过项；本轮不声称整套安装包已合规。

## 本轮工具验证（2026-11-05）

- 本地 `npm test`：248 passed、0 skipped；构建契约、Go 依赖边界、文档链接、shell 语法及 diff 检查通过。
- 在上述实际安装器的精确负载上运行总清点：286 项匹配；guest 范围内 32 项匹配，Android/Apple system 范围继续搁置；两个 ASAR 分别为 20/13 文件，无独立 npm 模块。
- 在实际 runtime 上运行 QEMU 工具：183 项记录，`authorizationReviewComplete=false`。
- 从两个哈希固定 `.deb` 生成计划：得到 busybox、glibc、linux-signed-amd64、linux 四个源包。
- 用 Go 1.25.0 在三个实际 Go 二进制上运行收集器：32 个第三方模块，未缺失许可证候选；`incomplete=true`。
- 未在锁定镜像内重跑四个 Debian 源包的下载与打包，未独立重建软件，未完成签名、逐文件许可证、静态重链接和对应源码完整性审核。

这些检查验证本轮工具及冻结记录，不会追溯改变 v1.0.1 的公开交付状态。
