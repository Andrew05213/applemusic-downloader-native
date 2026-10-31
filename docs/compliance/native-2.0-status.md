# 2.0 原生包第三方材料核查（未完成）

Native 1.0 新仓库承接下列旧项目 2.0 材料及未完成事项，不因清理 Git 历史而取得新授权。
本仓库尚未发布安装包，也未复制旧 Release 的源码附件。以下旧版本、哈希和发布状态均按历史证据理解，
不作为新仓库附件已交付的证明；当前源码迁移见[说明](../development/native-1.0-migration.md)。

本文件是工作清单，不是完整的第三方声明、对应源码包或法律意见。2.0 原生
公开构建开关已由维护者打开，但下列核查仍未完成；旧版 WSL 门禁仍关闭。
核查对象必须是实际交付的安装包及其 runtime，
而不是仅凭相同版本号推定。不要将本清单作为 GPL 合规证明。

现有收集工作聚焦非 Android/Apple 材料；未对 Android/Apple 材料作新的授权结论。
它们不因此获得授权。

2026-10-31 状态：仓库和 2.0.0 Release 已公开。公开是维护者的发布决定，
不是以下核查已完成的证据。安装包及部分源码资料的实际交付身份见
[正式版记录](../releases/2.0.0.md)。

## 已核对的构建输入

`native-runtime/inputs.lock.json` 与 `native-runtime/Dockerfile` 记录：

| 组件 | 锁定输入 | 待完成 |
| --- | --- | --- |
| QEMU Windows | 11.1.0，`qemu-w64-setup-20260811.exe`，SHA-512 见锁文件 | 获取该发行者对应的源码、补丁、构建脚本、配置及依赖来源；逐项核对实际 DLL 和固件。官方同版本源码不能单独证明对应关系。 |
| Debian Linux 内核 | `linux-image-6.1.0-51-amd64=6.1.177-1`，快照 `20260728T000000Z` | 已收集该版本 Debian 源码归档和补丁，并按 `.dsc` 校验归档 SHA-256；仍需确认二进制对应性、配置及源码交付。 |
| Debian BusyBox | `busybox-static=1:1.35.0-4+deb12u1+b1`，同一快照 | 已收集 `1:1.35.0-4+deb12u1` 源码归档和补丁并按 `.dsc` 校验 SHA-256；仍需核对二进制重建及源码交付。 |
| cJSON | commit `c859b25da02955fef659d658b8f324b5cde87be3` | [LICENSE](https://raw.githubusercontent.com/DaveGamble/cJSON/c859b25da02955fef659d658b8f324b5cde87be3/LICENSE) 为 MIT；完整文本已加入原生打包配置，仍需确认实际链接范围。 |
| Dobby | commit `9d271803881f467aabfc24593c7dcaeb710fe861` | [LICENSE](https://raw.githubusercontent.com/BepInEx/Dobby/9d271803881f467aabfc24593c7dcaeb710fe861/LICENSE) 为 Apache-2.0；完整文本已加入原生打包配置，仍需检查该树的其他许可证与 NOTICE。 |

上述 cJSON/Dobby 为构建输入，不能仅据此断言其全部代码都进入最终二进制。
`native-runtime/` 的 Dockerfile、`build-guest.sh`、`init`、补丁及锁文件是本项目
构建资料的一部分，但并不替代各上游完整对应源码。

## 2026-10-30 本地证据

- `.artifacts/debian-linux-busybox-source-partial-20261030.zip` 是 Linux/BusyBox
  的**部分**资料包，SHA-256 为 `c481c76c7312ebbbc45c0e4b104d026fb98bc746be9291312780290a0c16a74f`。
  ZIP 完整性和 `.dsc` 内四个归档哈希已核验；这不表示 `.dsc` 签名、
  二进制可复现性或完整源码提供已核验。
- `.artifacts/go-license-candidates-e-volume/inventory.json` 根据三个 Go
  二进制生成，列出 32 个模块及许可证候选文本，仍标记 `incomplete: true`。
- `.artifacts/qemu-license-inventory-20261030.json` 列出该次 runtime 的 183 个
  QEMU 文件及哈希；它不证明源码对应关系或各文件授权。
- 历史测试串行通过 681 项；这不是许可证审核或完整安装包源码交付的验证。

这些路径是被 Git 忽略的本机 E 盘产物，**不是仓库内容或公开下载地址**，
可能随本地清理丢失。发布维护者须单独保存、复核并按实际分发版本提供材料。

其中 Debian 部分源码 ZIP 与校验和已人工附加到[2.0.0 Release](https://github.com/Andrew05213/applemusic_downloader/releases/tag/v2.0.0)，
不再仅限本地；Go/QEMU 的候选清单仍是本地审查资料。该附件不构成整套安装包完整对应源码。

## 仍未完成的核查与交付

1. 以安装包 `native-package.json` 和 runtime `bundle.json` 为基础建立逐文件
   清单；覆盖 QEMU DLL、固件、guest 内容、Android 开源组件与实际编译进
   `amdl-desktop`、`amdl-service`、`amdl-guest` 的 Go 模块及其传递依赖。
2. 对每个分发的组件保存版权和完整许可证，核查是否要求 NOTICE、修改声明、
   源码或其他条件；不能把模块仓库根目录许可证一概视为所有文件的许可。
3. 为 GPL 组件取得与**实际二进制**对应的完整机器可读源码及相关构建/安装
   脚本、配置和补丁，验证来源与哈希，并选定适用于具体分发场景的提供方式。
   仅有上游链接、承诺或本清单不等于源码已经提供；不要提供无法履行的书面承诺。
   参见 [GPLv2 第 3 条](https://www.gnu.org/licenses/old-licenses/gpl-2.0.en.html)。
4. 审核 QEMU 发行者的具体构建及固件、DLL 的授权，并核查原始下载器源码和
   Android/Apple 运行材料的权利。无法核实的项目保持阻塞，不以猜测补许可。
5. 完善 2.0 专用 NOTICE 及其实际安装包清单。历史 NOTICE 已保留为
   `docs/legacy/NOTICE-v1`；旧 appliance 上下文及锁定哈希检查使用该副本。
   当前原生 NOTICE 仍明确标示不完整，不构成合规声明。
6. 将第三方声明及对应源码提供机制接入 2.0 打包和发布验证，再由有资质
   的人员确认具体交付方式。开关已打开不等于满足第三方许可条件。

QEMU Windows 发行者说明其源码中包含安装器构建脚本，见
[发行者页面](https://qemu.weilnetz.de/?C=M%3BO%3DD)；仍需取得并核对与所选
2026-08-11 二进制对应的具体来源。Debian 的 [BusyBox 源包信息](https://packages.debian.org/source/bookworm/busybox)
可作为定位线索，不能替代锁定快照中的哈希及实际包身份校验。

## 可复验的本地收集

`native-runtime/collect-debian-sources.sh` 供锁定构建镜像内运行，要求 `/out`
是 E 盘上的全新空目录。它从锁定快照收集指定 Debian 源包（含 `.dsc`、上游
源码、Debian 补丁），验证 `.dsc` 中的 SHA-256 并生成本地哈希清单；
不会自动解包或验证构建结果。
APT 的快照索引验证与 `.dsc` 签名验证是两件事：如果日志报告未知签名密钥，
不得声称签名已验证。还需核对实际二进制身份、构建配置及源码提供方式。
`node native-runtime/package-debian-sources.js <收集目录> <新zip>` 可复核归档
哈希并打出单独的 Debian 源码资料包和校验和；它明确标为部分材料，不包含
QEMU，也没有自动完成对最终用户的交付，不是整套安装包的完整源码提供。

`node native-runtime/collect-go-licenses.js <新输出目录> <二进制>...` 使用
`go version -m` 和模块校验和，收集实际二进制依赖的许可证候选文件；输出
必须位于本仓库 `.artifacts/` 下。清单的 `incomplete: true` 表示仍需审查每个
模块及子目录的版权、许可证和归属，不是最终声明。应传入与实际 runtime
证据匹配的三个 Go 可执行文件；主项目源码的授权未解决，也不会因依赖清单
齐全而自动获得授权。

`node native-runtime/inventory-qemu.js <runtime目录> <新输出文件>` 校验 runtime
来源和 manifest 后记录其 QEMU 文件与哈希；这些信息只用于逐项审查，并不能
证明文件已获授权或源码与构建完全对应。

发行者的 [QEMU 源码镜像](https://github.com/stweil/qemu) 是定位构建来源的
线索；没有发行者构建提交、MSYS2 包版本及子模块对应证据时，不能用同日提交
或官方同版本源码冒充完整对应源码。
