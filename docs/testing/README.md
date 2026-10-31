# 测试入口与记录索引

## 当前验证

从仓库根目录运行：

```powershell
npm run verify:native
npm run verify:boundaries
npm test
node scripts/check-docs.js --all
```

`npm test` 使用 `electron/native-ci.js` 的原生/共享集合，包含文档回归。
`npm run test:all` 扫描当前 `test/**/*.test.js`，`npm run test:electron` 只扫描
`test/electron/*.test.js`。归档的 `legacy/v1/test/` 和 `legacy/v1/e2e/` 不在这些入口中；
旧测试需在[完整的 1.x 维护树](../legacy/README.md#维护-1x)内准备和验证，不能直接从归档执行。
`verify:boundaries` 核对三份原生 Go 目标的实际依赖；当前模块的 Go 测试不再发现
归档的 Appliance/WSL 专属包，但共享 Web 回归仍保留并读取 `web/`。

UI 回归用 `npm run test:web`，首次运行前安装测试浏览器：

```powershell
npx playwright install chromium
npm run test:web
```

真实 AAC 解码用例另需支持 AAC 的浏览器和已生成的媒体夹具；未设置
`AMDL_STREAM_OUTPUT` 时会跳过这些用例，不能把普通 UI 通过当作完整播放验收。
夹具生成、FFmpeg 完整解码和 Chrome 流式播放的实际步骤以
`.github/workflows/verify.yml` 为准；`AMDL_STREAM_BROWSER` 可指定已安装的测试浏览器 channel。
普通验证不登录真实账号、不发短信、不下载商业媒体。

不要把测试总数写成永久保证；报告应记录实际执行的命令、提交和跳过范围。

## 历史证据

本目录保存各日期的测试和诊断记录。它们是历史证据，不是当前版本的完整验收声明。
其中安装包路径、测试数量、私有状态和未发布描述仅代表记录当时情况；本轮隔离不改写旧结论。
当前源码状态见[Native 1.0.0 基线](../releases/1.0.0.md)，旧发布身份见[旧仓库 2.0.0 记录](../releases/2.0.0.md)，当前测试命令见[开发入口](../development/README.md)。

主要历史主题：

- [第四阶段 JobAPI / 文件用例验收](2026-10-31-phase4-job-api-files.md)：句柄所有权、错误契约、文件身份与跨平台安全回归。
- [第三阶段共享 Go 接口验收](2026-10-31-phase3-shared-go-interfaces.md)：接口契约、CLI/Web 适配、Wrapper 宿主策略和跨平台回归。
- [第二阶段依赖隔离验收](2026-10-31-phase2-dependency-boundaries.md)：Go/Web、旧 Appliance/Wrapper 边界与跨平台回归。
- [流式播放验证](2026-10-28-native-fragment-streaming.md)。
- [并发、ALAC 与空目录](2026-10-28-v2.0.0-concurrency-package.md)。
- [M4A 索引与时长修复](2026-10-25-fragment-index-duration-fix.md)。
- [目录选择](2026-10-25-native-download-directories.md)、[任务删除](2026-10-24-native-task-deletion.md)。
- [窗口化启动与缓存](2026-10-24-native-ui-cache-windowed-package.md)。
- [历史 Beta 验收指南](v2.0.0-beta.1.md)。

新报告应标明提交、输入/产物身份、实际执行的检查和未验证范围；不应包含密码、Token、会话或未经脱敏的个人诊断。
