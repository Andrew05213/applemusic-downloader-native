# 开发与构建

所有命令从仓库根目录执行。请先阅读[仓库结构](../REPOSITORY.md)，修改共享后端前阅读[依赖边界](dependency-boundaries.md)。

## 环境

日常 CI 使用 Node.js 22、Go 1.25.0。桌面运行和 NSIS 安装器组装要求 Windows x64；Linux 可运行共享后端和 UI 测试。

完整 runtime 构建还需要 Docker Linux 引擎、`curl.exe`、7-Zip，以及 `native-runtime/inputs.lock.json` 指定的工具链镜像。7-Zip 默认查找位置为 `C:\Program Files\7-Zip\7z.exe`。

**完整构建目前不是仅凭 clone 即可复现的流程。** 工具链镜像的名称和 ID 已锁定，但仓库没有提供可直接取得该镜像的公共分发入口。缺少镜像时，不能任选镜像或跳过来源验证；这不妨碍阅读源码、运行单元测试和修改 UI。成品用户不需要 Docker 或 WSL。

## 安装依赖与日常验证

```powershell
npm ci
npm run verify:native
npm run verify:boundaries
npm test
npx playwright install chromium
npm run test:web
npm run build:native-backend
npm run build:native-service
```

真实 AAC 用例还需媒体夹具和支持 AAC 的浏览器，见[测试指南](../testing/README.md)。未设置夹具时的跳过不能视为完整播放验收。

项目脚本将开发临时文件和缓存放在仓库所在卷，不更改系统全局设置。Windows Go race 的环境适配和已知限制见测试指南。

## 构建运行组件

```powershell
npm run build:native-runtime -- --allow-network
```

按命令输出选择本次 runtime 目录。以下 `<本次输出目录>` 是占位符，必须替换：

```powershell
$env:AMDL_NATIVE_BUNDLE = (Resolve-Path '.artifacts/runtime/<本次输出目录>').Path
$manifest = (Resolve-Path (Join-Path $env:AMDL_NATIVE_BUNDLE 'bundle.json')).Path
npm run start:native -- --native-manifest $manifest
```

不带 `--allow-network` 的 runtime 用于隔离测试，不能用于联网发行包。构建和启动会检查锁文件、配方、guest 源码及文件身份；源码变化后应重建对应组件，不使用过期收据。

## 构建安装器

在已选择、验证联网 runtime 后：

```powershell
$env:AMDL_PUBLIC_RELEASE = '0'
npm run dist:native
```

此为本地测试模式，不上传附件。显式公开模式和发布步骤见[CI 与发布](../RELEASE.md)。`dist:win`、`dist:win:online`、`dist:win:offline` 只是 `dist:native` 的兼容别名。

安装包、资源阶段和测试缓存的位置见[构建存储](build-storage.md)。旧 WSL 构建不属于当前流程，`legacy/v1/` 仅供参考，见[归档说明](../legacy/README.md)。

修改文件操作时需保留[句柄与文件身份约束](job-api-file-boundaries.md)；不要用真实账号、验证码或下载的音乐作为自动化夹具。
