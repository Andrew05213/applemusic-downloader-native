# 第三方材料与源码提供状态

本页描述 Native 项目的现有资料和未完成核查，不是完整许可确认、源码提供承诺或法律意见。公开仓库及构建开关不授予第三方权利。

## 已发布资料

[Native 1.0.0 Release](https://github.com/Andrew05213/applemusic-downloader-native/releases/tag/v1.0.0) 附带：

- Windows x64 安装器及 SHA-256。
- `debian-linux-busybox-source-partial-1.0.0.zip` 及 SHA-256。
- `verification-1.0.0.json`，记录安装器身份、测试范围和限制。

Debian 部分资料包 SHA-256 为 `acb61042cae090736b2250d630bbf570dd782919e11766b3188c8581b7b56f89`，包含 Linux/BusyBox 源码归档、Debian 补丁和项目构建资料。四个源码归档按 .dsc 的大小和 SHA-256 校验。

该包**不含 QEMU 对应源码，不是整套安装包的完整对应源码提供**。.dsc 签名、二进制重建对应性及完整授权核查尚未完成。不得将本地未上传的资料视为已向用户交付。

## 锁定输入

组件身份以 `native-runtime/inputs.lock.json`、配方和实际安装器/runtime 回执为准。

| 组件 | 输入与现有资料 | 未完成事项 |
| --- | --- | --- |
| Wrapper Lite | WorldObservationLog/wrapper，commit `2fd7e0eaef081f93cf687428b965674937832ba4`；MIT 文本在 `native-runtime/notices/wrapper-LICENSE`，补丁在 native-runtime | 核对最终分发材料及相关声明 |
| QEMU Windows | 11.1.0，发行者安装器及 SHA-512 在锁文件，许可证文本随 runtime 保留 | 对应该发行二进制的源码、补丁、配置、构建脚本，以及附带 DLL/固件的来源与许可 |
| Debian Linux | `linux-image-6.1.0-51-amd64=6.1.177-1` | 完整构建配置、实际二进制对应性及交付核查 |
| Debian BusyBox | `busybox-static=1:1.35.0-4+deb12u1+b1`，已提供源包版本 `1:1.35.0-4+deb12u1` | 二进制重建与源码对应性核查 |
| cJSON | commit `c859b25da02955fef659d658b8f324b5cde87be3`，MIT 文本在 third-party-licenses | 实际链接范围和归属检查 |
| Dobby | commit `9d271803881f467aabfc24593c7dcaeb710fe861`，Apache-2.0 文本在 third-party-licenses | 树内其他许可、NOTICE 与分发范围检查 |
| Go / npm 依赖 | go.mod/go.sum、package-lock.json 及实际二进制 build info | 逐模块及子目录版权、许可证和声明审核 |
| 原始下载器 | 署名 Sorrow，来源线索见[上游清单](../UPSTREAMS.md)；runv2 MIT 文本保留 | 完整导入来源及原始代码再分发授权 |
| Android/Apple、设备/CDM 材料 | 不因本仓库源码许可获得权利 | 未作新的授权确认，需单独审查 |

## 对应源码与声明的剩余工作

以实际 `native-package.json`、runtime `bundle.json` 和文件哈希建立组件清单，覆盖 guest、DLL、固件和实际链接依赖；不能仅用相同版本号推定对应关系。

对需要源码的组件取得与实际二进制对应的完整机器可读源码、补丁、配置及构建/安装脚本，并按适用分发条件提供可取得的资料。上游链接、候选源码或无法履行的书面承诺不替代交付。GPLv2 具体条件见[许可证原文](https://www.gnu.org/licenses/old-licenses/gpl-2.0.en.html)。

QEMU 发行者[来源页面](https://qemu.weilnetz.de/?C=M%3BO%3DD)和[源码镜像](https://github.com/stweil/qemu)仅作为定位线索；没有具体构建提交、MSYS2 依赖和子模块证据时，不能用同版本官方源码冒充完整对应源码。

原始版权、许可证、修改声明和 NOTICE 需要与实际分发范围一并审查。[当前 NOTICE](../../NOTICE)明确保留未完成状态；[历史 NOTICE](../legacy/NOTICE-v1)保留原始字节供旧输入校验，不是当前安装器描述。

## 收集工具

下列工具生成审查候选资料，不自动完成授权核查或对用户的交付：

| 工具 | 输入和结果 |
| --- | --- |
| `native-runtime/collect-debian-sources.sh` | 在锁定构建镜像中运行，/out 指向新的空输出目录；收集固定快照源包并校验 .dsc 中 SHA-256 |
| `node native-runtime/package-debian-sources.js <收集目录> <新zip>` | 复验归档并生成部分 Debian 资料包及校验和；输出位于仓库 .artifacts 下 |
| `node native-runtime/collect-go-licenses.js <新输出目录> <二进制>...` | 基于 go version -m 和模块校验和收集实际二进制依赖的许可证候选 |
| `node native-runtime/inventory-qemu.js <runtime目录> <新输出文件>` | 校验 runtime 来源与 manifest，记录 QEMU 文件及哈希 |

APT 快照索引验证不等于 .dsc 签名验证。候选清单中的 incomplete 状态不能在未经审核时移除；应使用与发布 runtime 一致的实际二进制，不以维护者本地目录作为公开资料地址。收集资料的审查与发布检查见[发布指南](../RELEASE.md)。
