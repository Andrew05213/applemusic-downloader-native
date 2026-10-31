# 文档导航

当前源码版本：[Native 1.0.0](releases/1.0.0.md)，尚未发布本仓库安装包。
新仓库不导入旧 Git 历史，来源与兼容边界见[迁移说明](development/native-1.0-migration.md)。
公开发布不代表第三方源码与授权核查已经完成，详见[核查状态](compliance/native-2.0-status.md)。

## 使用软件

- [下载与安装](../README.md#下载与安装)：安装包、校验和及未签名提示。
- [用户指南与故障反馈](USER-GUIDE.md)：登录、播放、下载、设置与诊断。
- [1.0.0 源码基线](releases/1.0.0.md)：当前状态，不代表已经发布安装包。

## 开发和维护

- [开发与构建](development/README.md)：首次准备、源码运行和构建前置。
- [测试入口与记录](testing/README.md)：当前验证命令、真实 AAC 夹具与历史证据。
- [仓库结构](REPOSITORY.md)：当前入口、`legacy/v1/` 归档与 1.x 维护边界。
- [第二阶段依赖边界](development/dependency-boundaries.md)：原生 Go、共享 Web、旧 Appliance/Wrapper 的职责与保留输入。
- [共享 Go 接口与宿主适配](development/shared-go-interfaces.md)：第三阶段接口、首批迁移与尚未拆分的安全边界。
- [JobAPI 与文件用例边界](development/job-api-file-boundaries.md)：第四阶段 HTTP/存储隔离、句柄所有权和删除不变式。
- [构建存储](development/build-storage.md)：开发产物与本地清理。
- [CI 与发布流程](RELEASE.md)：显式公开模式、草稿审核和版本约束。
- [上游与依赖来源](UPSTREAMS.md)、[第三方核查状态](compliance/native-2.0-status.md)。
- [共享 Catalog API](CATALOG_API.md)：接口职责；不等于所有桌面能力的用户保证。
- [文档维护约定](DOCUMENTATION.md)：文档分层、链接检查与更新规则。

## 历史与研究记录

[历史索引](legacy/README.md)涵盖 1.x、旧 DRM 浏览器方案、短信原型、架构演进及日期化验收。
旧仓库的 [2.0.0 发布记录](releases/2.0.0.md)仅作历史证据，不是本仓库下载说明。
需要维护完整 1.x 时，从[独立检出步骤](legacy/README.md#维护-1x)开始，不直接运行 main 中的归档脚本。
历史文件中的路径、私有状态、测试数量和命令只代表当时情况，不作为当前安装或发布步骤。
