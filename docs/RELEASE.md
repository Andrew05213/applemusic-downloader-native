# Native 1.0 CI 与发布

当前入口是 `.github/workflows/verify.yml` 和 `release-native.yml`。
旧 WSL/Appliance 操作见[历史发布说明](legacy/RELEASE-v1.md)，不作为当前依赖。

## 分发边界

`native-runtime/release-policy.json` 的原生公开构建开关已由维护者设为 true；
历史 WSL 的 `appliance/release-policy.json` 仍为 false。开关不代表来源或许可
审核完成。不得上传账号、Token 或下载的音乐。
安装包仍未签名。更改仓库可见性或公开其他附件前，应检查所涉及的资料和发布范围。

本仓库从 1.0.0 开始，当前只提供源码，尚未发布安装包、标签或 Release。
[源码基线](releases/1.0.0.md)记录来源；旧 2.0.0 资产仍在旧仓库，不作为本仓库发布输入。

普通 CI 不上传运行组件或安装包。发布只创建新的 **draft prerelease**，
不会自动发布、覆盖旧标签/附件，也不会把安装包上传为 Actions artifact。

## 公开发布与未完成核查

本仓库可以公开源码，但**不能将现有材料标记为许可核查完成**。维护者了解风险
不等于取得第三方许可，也不使未完成的源码提供自动完成。具体证据和未完成
项目见[原生第三方材料清单](compliance/native-2.0-status.md)。

以下工作仍需完成：核实实际 QEMU 二进制、DLL 和固件的来源及对应源码；完成
Linux/BusyBox 源码与实际二进制的对应和可取得的交付；逐项完成 Go 依赖与
其他第三方声明；取得或替换原始下载器的授权；并审核其余随包材料的权利。
本轮未审查 Android/Apple 材料，不应将其标为已获授权。

`.artifacts/` 中的本地资料包不随 Git 推送，也不会由现有 Release 工作流
自动附带。公开分发方式、源码持续可取得性及许可条件需逐项复核并实际落实。
旧仓库的 2.0.0 发布曾附带明确标为不完整的 Debian 源码资料包；本仓库没有迁移这些附件，
不能推断自动工作流会上传它们，或已经提供所有组件的完整对应源码。
远端 Release 默认仅接受私有仓库；显式勾选 `public_release` 才允许公开模式。
打开原生构建开关不改变仓库可见性、不公开附件，也不等同于源码提供机制已建立。

本地原生构建如需显式启用公开构建模式，在选择 runtime 后设置
`$env:AMDL_PUBLIC_RELEASE='1'` 再执行 `npm run dist:native`。该模式仍执行现有
runtime 来源与安装包校验，不上传产物。生成包中的 `native-release-policy.json`
记录原生开关；保留的 `release-policy.json` 属于旧 WSL 策略，runtime 历史
build evidence 和 receipt 中的 false 则记录原有私有构建来源，不能被解释成
新的授权或许可证核查结论。

## 普通 CI

PR、main/master、`codex/native-ci-*` 推送及手动触发运行：

- Linux：Go 全量 race/vet、原生 guest 交叉编译、原生/共享 JS 和 UI 回归。
- 生成无账号的 42 秒 AAC HLS，经生产分片代码处理，FFmpeg 完整解码，
  再由支持 AAC 的 Chrome 测试真实流式播放，不依赖 Apple 网页播放器。
- Windows：原生 backend/service 编译、合成协议 smoke、JS 和 Go race；
  旧会话存储的 directory-Sync 用例由 Linux 全量覆盖。

两平台额外执行 `npm run verify:boundaries`，校验 service/control/guest 的实际 Go 依赖。
旧 Appliance/WSL 专属 Go 包已移出当前模块；共享队列、文件、Web 和原生媒体回归保留。

Windows 托管 runner 使用管理员默认文件所有者及可能包含短路径的 TEMP。
CI 专用脚本为当前进程设置用户默认所有者和仓库内长路径 TEMP，结束恢复；
不修改机器策略，不放宽产品的目录所有者/ACL 检查，也不跳过这些安全回归。

Runner 明确固定为 `ubuntu-24.04` 和 `windows-2025`（发布组装同用后者），
不跟随 `*-latest` 的系统迁移。镜像内工具仍由 GitHub 更新，不等于冻结整套镜像。
`actions/checkout` v7.0.1、`actions/setup-go` v7.0.0、`actions/setup-node` v7.1.0
均锁定完整提交 SHA，Action 自身使用 Node 24；GitHub 托管 runner 满足其最低版本要求。
项目命令仍使用 Node 22、Go 1.25.0，这与 Action 自身的运行时是两件事；
不启动 WSL、旧 rootfs、Docker Appliance 或 CDM 浏览器。
`npm test` 是原生/共享默认集合；`npm run test:all` 和 `npm run test:electron`
只发现当前 `test/` 下各自的测试。旧 WSL/DRM 专属测试已归档到 `legacy/v1/test/`，
不进入这些集合；完整 1.x 验证应使用[维护分支](legacy/README.md#维护-1x)。

## 显式原生 runtime 输入

托管 Windows runner 没有本机锁定的 Linux 构建镜像。发布只接受维护者显式
选定的 runtime ZIP：**同一仓库 Release asset ID** 和独立记录的 **SHA256**。
不自动猜测旧产物，也不改变成品的离线安装方式。

在受控 Windows 构建机按[当前构建入口](development/README.md#原生运行与安装包)
生成联网 runtime。Docker 仅用于这一独立生产步骤，不是普通 CI、NSIS 组装
或用户安装的依赖；来源与授权限制仍有效。

```powershell
npm run build:native-runtime -- --allow-network
# 用本次实际输出替换占位符，不能使用隔离包或陈旧组件。
npm run release:native -- pack .artifacts/runtime/<本次目录> .artifacts/runtime-input/amdl-native-runtime-<唯一标识>.zip
```

`pack` 校验源码来源，仅收录 bundle.json、guest、qemu、notices、evidence，
输出 SHA256。在允许的范围，将 ZIP 上传到**同仓库专用草稿 Release**，
记下 API 返回的 asset ID，保留上述哈希作为独立预期输入。脚本不替你上传
runtime、配置 runner 或建立镜像仓库。

发布验证 API 中的资产 ID、名称、大小、digest，再核对下载字节哈希。
解压前拒绝路径穿越、链接、重复路径和超限大小；解包后重验联网 manifest、
锁文件、recipe、guest-agent 源码集合和运行文件身份。过期就重新生成。

## 手动发布

1. 提交推送目标源码，按需更新版本；本地运行必须使用干净工作树。
2. Actions 选择 **Native Release**，选定目标分支/提交，输入
   `runtime_asset_id`、`runtime_sha256`。
3. 默认 `create_draft=false`：先跑完整 CI，再构建原生 x64 NSIS，解出安装内容
   逐文件审核、核对校验和；不创建标签/Release，产物随 runner 清理。
4. 通过后明确勾选 `create_draft=true` 重新运行，创建 `v<package.json版本>`
   草稿 prerelease，仅附 EXE、SHA256；说明记录源码/runtime/installer 身份。
   **已有版本直接拒绝，不能原地替换。**
5. 默认 `public_release=false`，要求私有仓库；选择公开模式须明确勾选
   `public_release=true`。人工审阅草稿；工作流不会自动发布或更改仓库可见性。

仓库目前为公开状态，默认私有模式会拒绝运行，需要有意选择公开模式。
新仓库尚无版本标签。首次安装包发布需单独授权；只创建新标签，不改动旧仓库的 v2.0.0、
分支或附件，也不把旧版本号重编号为已验证的 1.0.0 安装包。

使用现有 `GITHUB_TOKEN`，只有 installer job 获得 contents:write，不需新 PAT。
可为 `native-private-release` environment 配置审批人和分支限制；**单独写出
environment 名称不代表审批已经启用**。

本地等价验证（需 gh 登录）：

```powershell
$env:GITHUB_REPOSITORY = 'Andrew05213/applemusic-downloader-native'
$env:AMDL_RUNTIME_ASSET_ID = '<资产ID>'
$env:AMDL_RUNTIME_SHA256 = '<64位小写SHA256>'
$env:AMDL_CREATE_DRAFT = 'false'
$env:AMDL_PUBLIC_RELEASE = '1' # 本仓库为公开仓库；仍需明确选定已验证 runtime
npm run release:native -- run
```

CI 不执行真实登录、发短信或下载商业媒体；通过不等于任何真实账号、订阅或地区可用。
