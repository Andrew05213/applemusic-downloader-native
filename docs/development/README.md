# 从 clone 到运行与打包

本指南面向 Windows x64。只需要标准开发工具，不需要旧仓库、维护者的 Docker 镜像、账号目录或已有 runtime。

## 1. 安装前置工具

- Git。
- Node.js 22.12 或更高的 22.x（含 npm）。
- Go 1.25.0，与 guest 工具链锁一致。
- Docker Desktop，使用 Linux containers，并等待引擎就绪。

这些工具由开发者自行安装，项目不会自动申请管理员权限或改系统配置。首次准备需要网络和数 GB 空间；Docker 的镜像存储位置由 Docker Desktop 管理。不要为构建而关闭 TLS、来源或哈希校验。

不需要安装 WSL 应用、系统 7-Zip、Android Studio、CMake、NDK、FFmpeg 或 MP4Box。完整 7-Zip 从锁定官方归档取得，只解包、不运行安装器；归档和实际工具分别校验，许可证保留在本地工具目录。Linux 工具链由下一步自动构建。Docker Desktop 自身使用的虚拟化后端与成品依赖是两回事。

## 2. 克隆与安装依赖

建议使用空间充足、长度适中的路径，不要放进多层嵌套的工作树：

```powershell
git clone --depth 1 https://github.com/Andrew05213/applemusic-downloader-native.git
cd applemusic-downloader-native
$env:ELECTRON_CACHE = Join-Path $PWD '.tmp-v2-native/development/electron-cache'
npm ci --cache .tmp-v2-native/development/npm-cache
npm run doctor:native
```

环境检查逐项显示 Windows/Node/Go/Git/Docker/Buildx/Electron 包/7-Zip 引导工具状态及修复方法。不要从其他工作区复制 node_modules。[Electron 45 采用按需下载](https://www.electronjs.org/docs/latest/tutorial/installation)，setup 会显式安装锁定版本的二进制；npm ci 后尚无 electron.exe 是正常状态。

浅克隆包含维护当前版本所需源码。已外置的旧版二进制不属于 Native 构建输入；普通开发不需获取它们。完整历史仍保留旧文件，`git fetch --unshallow` 会重新下载历史对象。任务入口和二进制存储规则见[贡献指南](../../CONTRIBUTING.md)。

## 3. 验证与启动

```powershell
npm test
npm run setup:native
npm run dev:native
```

setup 会自动：

1. 检查前置环境。
2. 安装并校验锁定版本的 Electron 二进制（首次）。
3. 从公开、锁定输入构建工具链镜像（首次）。
4. 下载并校验 Lite/QEMU 输入，编译 guest 并导出联网 runtime。
5. 编译 Windows backend/service，记录本次明确选定的 runtime。

dev 会检查所选 runtime 的来源和完整性，必要时生成新组件，再启动开发界面。不需要复制 manifest 路径；不会扫描其他工作区寻找旧包。关闭应用后命令结束。使用自己的授权账号测试时，不将密码、验证码或诊断中的个人信息提交到 Git。

已有有效组件时可直接运行 dev。setup/dev 的 Go 缓存在当前仓库卷，不需要维护者的 GOPATH；安装后的数据路径不因此改变。

## 4. 构建安装包

```powershell
npm run package:native
```

该入口复用通过来源验证的**联网** runtime，编译所需服务并调用现有 dist:native 流程。输出为 `.artifacts/installers/<版本>-<唯一标识>/` 中的 EXE、SHA-256 和构建证据；不上传、不创建标签，也不替换已发布附件。默认本地测试模式。

首次打包还会从锁定来源预取 WinCodeSign、NSIS 及 NSIS resources，并核对归档和解包内容；传输失败会重试。无需从维护者机器复制 electron-builder 缓存。

安装包未签名。正式发行还需[发布审核](../RELEASE.md)，不能把打包成功视为授权、真实账号或覆盖安装验收已完成。

## 工具链如何取得

默认不拉取任何项目私有镜像，也不使用维护者本机镜像 ID。仓库内 `native-runtime/toolchain/` 提供完整构建入口：

```powershell
npm run build:native-toolchain
# 镜像被手工清理，或需要重新执行配方时：
npm run build:native-toolchain -- --no-cache
```

公开基础镜像固定到 linux/amd64 manifest 摘要；Debian 使用固定快照；NDK/CMake 校验 SHA-256；cJSON/Dobby 固定 Git commit 和递归子模块。当前保留的工具版本为 NDK r23b、CMake 3.25.3，版本身份见锁文件。

Debian 签名索引优先从 Fastly 快照入口读取，超时或失败时再尝试 Cloudflare 和原站，三个入口使用相同的快照日期。包名和版本只由快照索引决定；下载可使用 Debian CDN 或快照入口，每个文件都必须符合签名索引中的大小和 SHA-256。网络步骤设有时间上限，无法取得精确文件时停止构建。

构建记录绑定配方/锁文件哈希与实际镜像 ID，缓存命中后仍复验；guest 编译和组装在断网模式执行。工具链不包含 Apple runtime、账号或旧测试镜像。它只在开发构建中使用，不随成品安装。

记录是本地完整性与新鲜度证据，**不是签名证明，也不承诺不同机器构建出相同 Docker image ID**。有意更新配方会使旧 runtime 失效并要求重建，不能改标签冒充相同输入。独立工作流 `Native Toolchain From Source` 从公开输入重新构建，不发布镜像或 NDK 附件。

## 隔离验收与底层入口

无账号验收可生成独立隔离 runtime：

```powershell
npm run setup:native -- --isolated
```

隔离选择与联网选择分别记录，package 不会使用隔离包。真实合成 AAC、打包应用与文件删除验收见[测试指南](../testing/README.md)。

高级使用仍支持显式选择组件：

```powershell
npm run build:native-runtime -- --allow-network
# 用上一步实际输出替换占位符
$env:AMDL_NATIVE_BUNDLE = (Resolve-Path '.artifacts/runtime/<本次输出目录>').Path
$manifest = (Resolve-Path (Join-Path $env:AMDL_NATIVE_BUNDLE 'bundle.json')).Path
npm run start:native -- --native-manifest $manifest
npm run dist:native
```

单元、UI、Go 与跨平台验证见[测试指南](../testing/README.md)。架构见[仓库结构](../REPOSITORY.md)和[依赖边界](dependency-boundaries.md)；安装/数据兼容名称见[兼容说明](compatibility.md)。

## 常见故障

| 提示或现象 | 处理 |
| --- | --- |
| Docker engine / Buildx 检查失败 | 启动 Docker Desktop，切换 Linux containers，等待引擎运行后重试 |
| Go 版本不匹配 | 安装 Go 1.25.0，重新打开终端核对 PATH |
| 依赖下载失败 | 检查 GitHub、Google 下载站、Docker Hub、Debian snapshot 和 Go 模块网络；需要代理时使用工具官方的代理配置，不绕过证书或校验 |
| 输入 hash 或配方身份不匹配 | 停止使用该文件/镜像，核对来源；不要修改锁文件凑匹配 |
| 本地工具链镜像被清理 | 显式执行 build:native-toolchain -- --no-cache 后重试 |
| runtime 过期 | setup 会生成新的目录，旧证据不覆盖；不复制旧安装器中的组件来打包 |
| QEMU 无法读取 BIOS | 检查目录长度，避免深层嵌套；不要移动个人会话来当测试夹具 |
| 首次启动停留在启动画面 | 首次需完整校验、复制空白运行数据并启动组件；失败后可进入设置导出诊断并重试。不要关闭完整性校验，也不要删除账号目录来排错 |
| 编译/打包失败 | 保留版本、失败阶段和脱敏日志；不要上传整个账号目录 |
| npm audit 报告安全告警 | 执行 npm run audit:native；CI 阻止 moderate 及以上。构建成功不等于依赖审计通过，不使用 audit fix --force 降级或忽略开发依赖 |
| native_build_compat_* | 重新执行 npm ci；如有意使用 --ignore-scripts，随后执行 npm run prepare:build-deps。版本或哈希仍不匹配时停止构建并核对锁文件，不手动绕过校验 |
| native_build_dependencies_linked | node_modules 是共享链接；在独立 clone 安装依赖。不要直接用 npm ci 清理共享目标，若需移除链接，先确认操作对象只是链接而不是目标目录 |

### npm 依赖审计

`npm run audit:native` 检查包括开发依赖在内的完整依赖树，CI 在出现 moderate、high 或 critical 时失败。不要通过省略开发依赖、重命名易受影响的实现或关闭审计掩盖告警。结果会随公告更新，以当天输出为准。

依赖覆盖锁定真实上游 `minimatch 11.0.3` 和现代异步扫描链的 `glob 14.0.3`，分别包含 [globparts 放大修复](https://github.com/isaacs/minimatch/commit/77a0f5c)和[取消后停止扫描修复](https://github.com/isaacs/node-glob/commit/4682e8c)。包名、版本和注册表完整性保持原样，不使用审计排除表。

少数旧构建工具尚未适配这些接口。`npm ci` 的 `postinstall` 会运行 `scripts/npm-build-compat.js`，仅适配两类旧调用：ASAR/dir-compare 的默认函数导入，以及 glob 7 对模式对象的读取与测试。旧 glob 7 的同步/回调接口保留，匹配仍交给安全版 minimatch 的非递归实现，不转回旧正则算法。适配清单及前后 SHA-256 在 `scripts/npm-build-compat.lock.json`；未知版本、改动文件或共享 node_modules 链接均拒绝修改。重复执行会核对已适配内容，`verify:native` 和实际打包也会只读复验。

更新依赖时应同步检查是否可以移除适配；不可只修改预期哈希凑匹配。回归测试覆盖漏洞输入、扫描取消、旧 glob 同步/回调、ASAR 打包/解包、目录过滤和临时文件清理。发行仍只支持 Windows 原生 NSIS，不据此宣称已验证 macOS 或 Squirrel 安装器。上述依赖用于构建，不进入原生桌面运行时；构建仍应只使用可信源码和模板。

更多存储与清理规则见[构建存储](build-storage.md)。第三方材料仍有未完成核查，见[状态清单](../compliance/native-status.md)。

旧源码仅供[归档参考](../legacy/README.md)，`legacy/v1/` 不是本指南的构建输入。
