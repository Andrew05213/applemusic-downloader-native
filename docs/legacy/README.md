# 旧源码参考

`legacy/v1/` 保存前身项目的旧桌面、WSL/DRM 工具、配置、源码和专属测试。这里的旧版 1.x **不是本仓库 Native 1.0**。

归档不是独立可构建或可运行的工程，原相对路径只保留历史语境。不要在归档目录直接运行 npm、Docker、WSL 或预编译程序，也不要将归档说明作为当前安装指南。当前流程见[开发与构建](../development/README.md)。

## 来源与校验

- [桌面归档清单](../../legacy/v1/manifest.json)：旧维护基线、原路径、归档路径及 99 个文件的 SHA-256。
- [Appliance/Wrapper 归档清单](../../legacy/v1/phase2-manifest.json)：原始 242 个旧文件及 9 个共享 Web 资源的路径和哈希，其中 100 个预编译文件已移到独立 Release 附件。
- [历史第三方声明](NOTICE-v1)：原始内容由旧配方和校验引用，不是当前安装包描述。

这些清单是来源记录，不表示本仓库提供旧维护分支。原始许可证和锁定输入保留原文；技术文档中的现行结论见[依赖边界](../development/dependency-boundaries.md)。

日期化调试记录、旧发布说明和实施计划不作为当前开发文档保留。维护当前项目无需访问旧私有仓库。

## 按需获取旧二进制

100 个旧 ELF 文件（约 116 MiB，压缩后约 46 MiB）已放入[独立的旧运行材料 Release](https://github.com/Andrew05213/applemusic-downloader-native/releases/tag/legacy-binaries-20261104)，不混入产品安装包，也不影响 1.0.1 的 Latest 标记。

日常 Native 构建无需下载。确实需要查阅旧运行材料时，在完成 `npm ci` 后执行：

```powershell
npm run legacy:fetch
```

工具按[附件锁文件](../../legacy/v1/binary-assets.lock.json)中的固定版本、大小和 SHA-256 获取归档，解包前检查路径与大小，再逐文件核对原始 phase2 清单。结果写到 `.artifacts/legacy-binaries/<唯一目录>/unpacked/`，不会恢复到源码目录，不会运行归档程序，也不会下载或写入账号数据。

传输失败或校验不符时停止使用该文件，不修改锁文件凑匹配。离线维护者可从同一 Release 手工获取 ZIP 并根据锁文件校验；原始 manifest 继续保留在 Git 中。

这是当前源码树的二进制外置，不是 Git 历史重写。完整历史仍含旧对象；普通维护建议 `git clone --depth 1`。
