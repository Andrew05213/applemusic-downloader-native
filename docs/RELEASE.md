# CI 与发布

工作流入口为 `.github/workflows/verify.yml` 和 `release-native.yml`。正式安装包位于[本仓库 Releases](https://github.com/Andrew05213/applemusic-downloader-native/releases)，版本来自 `package.json`。

## 普通 CI

- Linux：Go 全量 race/vet、guest 交叉编译、原生/共享 JS、UI 与真实合成 AAC 回归。
- Windows：backend/service 构建、协议 smoke、JS 与 Go race。旧 Wrapper 会话的 directory-Sync 限制由 Linux 覆盖。
- 两平台运行依赖边界检查；不启动 WSL、官方网页或真实账号登录，不下载商业媒体。

runner 和 Actions 提交锁定在工作流内，项目工具使用 Node.js 22、Go 1.25.0。测试步骤见[测试指南](testing/README.md)。普通 CI 不上传 runtime 或安装包。

## 构建与分发前置

完整 runtime 构建需要锁定镜像及输入，当前可复现性限制见[开发指南](development/README.md)。成品用户无需 Docker 或 WSL。

原生公开构建由 `native-runtime/release-policy.json` 及显式 `AMDL_PUBLIC_RELEASE=1` 控制。旧 `appliance/release-policy.json` 保持关闭并作为兼容资料保留。开关不授予第三方权利，也不代表对应源码或授权核查完成，详见[核查状态](compliance/native-status.md)。

安装器未签名，说明和校验文件必须如实标注。不得上传账号、验证码、Token、会话镜像或下载的音乐。

## runtime 输入

托管 Windows runner 只组装安装器，不包含锁定的 Linux 构建镜像。发布接受明确指定的**同仓库 Release asset ID** 和独立记录的 SHA-256，不自动选择最近目录。

先按开发指南构建联网 runtime，再打包。下列尖括号是必须替换的占位符：

```powershell
npm run release:native -- pack .artifacts/runtime/<本次目录> .artifacts/runtime-input/amdl-native-runtime-<唯一标识>.zip
```

`pack` 校验来源，仅收录 bundle.json、guest、qemu、notices、evidence。维护者在确认分发范围后将 ZIP 上传至同仓库专用草稿，记录 asset ID 和独立哈希。

消费端核对 API 元数据、名称、大小、digest 与下载字节；解包拒绝穿越、链接、重复路径及超限大小，并重验 manifest、配方、锁文件和 guest 源码身份。过期输入必须重建。

## 创建草稿

1. 更新版本与相应说明，提交目标源码，确认 CI 全部通过；本地发布要求干净工作树。
2. Actions 选择 **Native Release**，输入 `runtime_asset_id`、`runtime_sha256`。公开仓库必须显式选择 `public_release=true`；默认私有模式会拒绝公开仓库。
3. 默认 `create_draft=false`：运行完整 CI、构建和解包审核，不创建标签或 Release；runner 结束后产物清理。
4. 检查通过后设置 `create_draft=true` 重新运行，创建新的 `v<版本>` 草稿 prerelease。脚本默认只附 EXE 和 SHA-256。
5. 已有标签或同版本 Release 会被拒绝，不原地替换或移动旧标签。

工作流只创建草稿，不自动公开发布、修改仓库可见性或上传 Actions 安装包 artifact。只有 installer job 获得 contents:write；`native-private-release` environment 名称保留为兼容配置，审批人和分支规则需在仓库设置中单独配置。

本地等价验证：

```powershell
$env:GITHUB_REPOSITORY = 'Andrew05213/applemusic-downloader-native'
$env:AMDL_RUNTIME_ASSET_ID = '<资产ID>'
$env:AMDL_RUNTIME_SHA256 = '<64位小写SHA256>'
$env:AMDL_CREATE_DRAFT = 'false'
$env:AMDL_PUBLIC_RELEASE = '1'
npm run release:native -- run
```

## 发布正式版

人工审核草稿，确认：

- 标签目标等于已验证源码，EXE、版本说明、SHA-256 与解包证据一致。
- 上传后逐项核对服务端文件大小与 SHA-256，不只看上传成功。
- 必要的许可证、来源和源码资料已随该版本实际交付；现有脚本**不会自动收集或上传完整对应源码**。
- 发布说明列出实际验证范围、已知限制和未完成核查，不以 CI 替代真实账号、覆盖安装或授权验收。

审核后再取消 draft/prerelease 标志，将其发布为正式版；不要对旧版本执行覆盖上传。1.0.0 的安装器、部分 Debian 源码包和验证报告见[固定版本记录](releases/1.0.0.md)。部分源码附件不等于完整安装包对应源码。
