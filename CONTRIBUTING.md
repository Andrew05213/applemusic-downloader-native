# 维护与贡献入口

## 先验证，再选择工作范围

新贡献者建议浅克隆，避免下载已从当前源码树移除的旧版二进制历史：

```powershell
git clone --depth 1 https://github.com/Andrew05213/applemusic-downloader-native.git
cd applemusic-downloader-native
npm ci --cache .tmp-v2-native/development/npm-cache
npm test
npm run verify:native
```

Node.js 使用 22.12 或以上的 22.x，Go 使用 1.25.0。上述 JS 单元测试不需要启动 Docker、WSL、Apple 账号或旧运行组件。CI 还会检查 npm 安全告警、Go 依赖边界、race/vet 及合成音频 UI 回归，具体见[测试指南](docs/testing/README.md)。

Windows 上运行、调试和打包完整桌面版还需要 Docker Linux 引擎，请按[开发指南](docs/development/README.md)执行 `setup:native`、`dev:native` 和 `package:native`。Electron、Linux 工具链和运行组件按锁定输入准备，不复制他人的 node_modules 或缓存。

## 代码从哪里看

| 要修改什么 | 入口 |
| --- | --- |
| 窗口、系统对话框和文件夹定位 | `src/main/native-main.js`、`src/preload/` |
| 页面、导航和播放界面 | `src/renderer/native-*` |
| 下载任务、目录 API、文件与元数据 | `apple-music-downloader/` |
| guest 控制、登录会话 | `apple-music-downloader/internal/guestagent/`、`internal/nativeauth/` |
| runtime、工具链、安装包和 CI | `native-runtime/`、`electron/`、`.github/workflows/` |
| 共享 Web/CLI 与旧版边界 | [依赖说明](docs/development/dependency-boundaries.md) |

不要按名称批量删除旧 Wrapper 或共享 Go 文件；部分仍属于 CLI/Web 兼容层。源码职责见[仓库结构](docs/REPOSITORY.md)。

## 二进制和生成文件放哪里

- 当前运行输入在 `native-runtime/` 锁文件中声明，普通构建按开发指南获取。
- `.artifacts/` 存放安装包、验证输出和可选的旧二进制缓存；`.tmp-v2-native/` 存放开发缓存。两者不提交 Git。
- 冻结的旧版二进制已外置。Native 开发不需要它们；仅需查阅旧材料时见[归档说明](docs/legacy/README.md)。
- 新增大型二进制应使用独立 Release 附件和锁定哈希，不放回源码或 Git LFS 指针。
- 本轮不重写历史。需要完整提交历史时可执行 `git fetch --unshallow`，但这会同时下载历史二进制。

提交前补充相关回归测试，运行 `npm test`、`npm run verify:native` 和 `npm run verify:boundaries`。不提交账号、Token、会话、下载的音乐或本机路径；不要覆盖已有 Release 附件或移动已发布标签。
