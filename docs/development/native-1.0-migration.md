# Native 1.0 新仓库与兼容边界

## 来源和版本

新仓库为 `Andrew05213/applemusic-downloader-native`，产品版本从 `1.0.0` 开始。
旧仓库 `Andrew05213/applemusic_downloader` 的快照
`81e5c388347478b19e99137777abab33e769ca61` 是导入基线，含 JobAPI/文件用例解耦。

用 `git archive` 导出已跟踪源码后建立独立 Git 仓库，因此没有旧 Git 对象、提交祖先、分支或标签。
旧仓库的 main、维护分支、`v2.0.0` 及 Release 均未改变。
初始导入只公开源码并验证 CI；后续安装包另行构建验证，身份以[版本说明](../releases/1.0.0.md)及本仓库 Release 为准。

## 有意保留的兼容标识

- Go module 继续使用 `github.com/Andrew05213/applemusic_downloader`。
  这是源码内部模块路径，不要求从旧私有仓库下载本模块；仓库内构建使用本地源码。
  不批量改模块路径或 go.mod/go.sum，以免改变锁定 guest 的源码来源验证。
- NSIS GUID、appId、账号/会话/下载默认目录标识保持原样；版本重编号不迁移用户资料。
  带 `v2-test` 或 `2.0 Test` 的路径是历史兼容标识，不是当前产品版本。
- `native-runtime/`、平台安全校验、下载、标签处理和分片播放实现不变，协议版本也不重编号。
- `legacy/v1/` 的“1.x”指旧仓库的旧桌面/WSL 版本，不是 Native 1.0。
  归档及原始许可证、NOTICE、来源哈希继续保留，部分材料仍被兼容回归读取。
  清理 Git 历史不等于删除版权、授权责任或构建输入。

## 文档和分发

当前入口使用新仓库和 1.0.0；旧发布记录、测试报告及第三方材料保留原始版本、提交和哈希，
只作为历史证据。旧仓库目前的访问权限与新仓库独立，历史链接可能需要维护者权限。

初始导入没有复制旧 Release 资产或 CI secrets。普通 CI 不需要账号、旧仓库权限或生产 runtime；
发布时必须重新选取本仓库内具有对应来源证据的 runtime 资产，不能直接使用旧 asset ID。
第三方对应源码和授权仍未完成，现有公开构建开关不作许可结论。

1.0.0 发包在新工作区重新构建 runtime，并重新校验 Linux/BusyBox 源码候选后生成部分资料包；
这不是复制旧安装包改名，也不证明 QEMU、DLL、固件或整套运行组件的完整对应源码已经提供。

1.0.0 小于旧产品的 2.0.0；首次打包时应另测覆盖安装/版本回退及数据保留。
本轮保留标识但没有安装包验收，不宣称从旧安装包覆盖安装已经验证。

## 验证入口

```powershell
npm ci
npm run verify:native
npm run verify:boundaries
npm test
node scripts/check-docs.js --all
npm run build:native-backend
npm run build:native-service
```

Linux CI 运行 Go 全量 race/vet、合成 AAC 分片与 FFmpeg 完整解码、Chrome 真实播放回归；
Windows CI 运行原生服务构建、合成后端 smoke 与 Go 检查。当前工作流见[发布流程](../RELEASE.md)。
普通 UI 回归可用 `npm run test:web`；真实 AAC 夹具和浏览器要求见[测试入口](../testing/README.md)。
不以本地测试代替新仓库实际远端 CI，也不读取账号或下载商业音乐来做本轮验收。
