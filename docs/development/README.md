# Native 1.0 开发与构建入口

从[仓库首页](../../README.md)所在目录运行命令，确认当前检出的是 Native 1.0 源码；不要混用其他工作树的产物。
新仓库承接旧项目 2.0 实现但重新计版本，标识与依赖兼容限制见[迁移说明](native-1.0-migration.md)。

## 首次准备

日常 CI 使用 Node.js 22 和 Go 1.25.0。完整桌面运行与 NSIS 组装要求 Windows x64；
Linux 可以运行共享后端和 UI 测试，但不能用作当前 Windows runtime/安装器的完整构建宿主。
先完成下列无账号检查，再准备运行组件，不必为阅读或修改 UI 安装 WSL。

## 日常验证

```powershell
npm ci
npm run verify:native
npm run verify:boundaries
npm test
# 首次运行 UI 测试前准备测试浏览器
npx playwright install chromium
npm run test:web
npm run build:native-backend
npm run build:native-service
```

项目测试命令将临时文件与缓存放在仓库所在卷。Windows Go race 测试需使用 ASCII 临时目录，避免链接器受中文路径影响；已有历史 Windows directory-Sync 测试限制见对应 `docs/testing/` 记录，不应为了整理目录而跳过新的失败。

`npm test` 默认覆盖原生桌面与共享模块，不要求历史 WSL helper 产物。
`npm run test:all` 扫描当前 `test/` 下的全部 JS 单元测试，`npm run test:electron`
扫描当前 Electron 单元测试；二者均不包含 `legacy/v1/test/`，也不代表完整 1.x 验收。
UI 测试的真实 AAC 夹具、浏览器前置与远端覆盖见[测试入口](../testing/README.md)。
远端矩阵和原生 NSIS 草稿发布入口见[原生发布说明](../RELEASE.md)。
共享后端修改前先阅读[接口与宿主适配边界](shared-go-interfaces.md)，不要把旧 Web/Wrapper 的启动策略接入原生媒体路径。
涉及任务 API 或文件操作时，再阅读[JobAPI 与文件用例边界](job-api-file-boundaries.md)，保留已打开句柄和批次删除的所有权规则。

## 原生运行与安装包

```powershell
npm run build:native-runtime -- --allow-network
# 将下面路径替换为上一条命令的实际输出；不要照抄尖括号。
$env:AMDL_NATIVE_BUNDLE = '.artifacts/runtime/<本次输出目录>'
# 本地默认按私有测试模式构建，不上传安装包
$env:AMDL_PUBLIC_RELEASE = '0'
npm run dist:native
```

开发运行要求 manifest 的绝对路径，不能将相对路径或占位符直接传入：

```powershell
$manifest = (Resolve-Path (Join-Path $env:AMDL_NATIVE_BUNDLE 'bundle.json')).Path
npm run start:native -- --native-manifest $manifest
```

构建需与 `native-runtime/inputs.lock.json`、`native-runtime/guest-agent.lock.json` 匹配的 Go、
Linux 工具链镜像和其他输入；Windows 宿主还需 Docker Linux 引擎、`curl.exe` 和
默认位于 `C:\Program Files\7-Zip\7z.exe` 的 7-Zip。这些不一定随普通 clone 提供。
仅安装 Docker 不等于已有锁定镜像，缺少时不能挑选任意镜像或绕过来源检查。
Docker 仅用于开发构建，成品无需安装 Docker 或 WSL。

`npm run build:native-runtime` 不带 `--allow-network` 会生成隔离验收用的 runtime，不能当作用户联网安装包输入。构建脚本输出目录和产物证据是真实来源，不采用“挑一个旧目录凑包”的做法。

## 旧版隔离后的入口

- `npm run dist:win` / `dist:win:online` / `dist:win:offline` 都转到 `dist:native`，不是三种不同的当前安装包。
- 旧 WSL 构建、smoke 和 Appliance 准备命令已从根 `package.json` 移除，不能再在 main 上运行。
- 旧 Docker/Web 助手现位于 `legacy/v1/start.bat`、`legacy/v1/start.sh`；旧根 Dockerfile 和桌面打包配置也在该归档中，仅供查阅。
- 旧 Appliance/Wrapper 内容和专属 Go 命令已继续归档，Web 兼容界面独立在 `web/`；原生打包仍保留旧策略输入，详见[依赖边界](dependency-boundaries.md)。
- 需要完整旧版环境时，按[1.x 维护说明](../legacy/README.md#维护-1x)从旧仓库单独检出维护分支；新仓库不包含该分支，不要覆盖当前工作区。
- [历史 README](../legacy/README-v1.md) 中的命令保留其原始语境，不按当前 main 的路径解释。

## 不做的事

不全局改 TEMP、Go、Docker 数据位置；不移动安装后的用户资料；测试不自动使用真实账号登录或发送验证码。
公开构建开关及私有/公开草稿模式见[发布流程](../RELEASE.md)，不将技术开关当作第三方许可结论。
