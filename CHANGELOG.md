# 更新日志

本文件记录开发中和正式版本的用户可见变化。预发布及更早版本的原始说明见 [Releases](https://github.com/Andrew05213/applemusic-downloader-native/releases)。

## 未发布

- Wrapper Lite 升级至维护分支的 `80dcaa1`，适配新的 DNS 和 CA 配置逻辑；原生认证交互、下载和流式播放接口不变。
- Lite HTTP 与 Android SDK 使用同一份哈希锁定的 CA 证书，保留严格证书及主机名校验，不允许调试开关关闭 TLS 校验。
- guest 构建增加离线 DNS/TLS 回归，校验脚本也纳入运行时来源与可复现性记录。以上更改不影响已发布的 1.0.4 附件。

## 1.0.4 — 2026-11-13

[发布说明](https://github.com/Andrew05213/applemusic-downloader-native/releases/tag/v1.0.4) · [Windows x64 安装包](https://github.com/Andrew05213/applemusic-downloader-native/releases/download/v1.0.4/AppleMusicDownloader-1.0.4-Setup.exe) · [SHA-256 校验文件](https://github.com/Andrew05213/applemusic-downloader-native/releases/download/v1.0.4/AppleMusicDownloader-1.0.4-Setup.exe.sha256)

### 下载恢复

- Music 会话临时复验失败时，暂停受影响的下载并显示“等待会话恢复”；复验恢复后自动续跑未完成曲目，保留已成功文件及独立的资源错误。
- 恢复下载时重新获取当前凭据；等待期间可以取消任务，不会自动发起密码登录。
- 区分临时复验失败与明确的授权拒绝，避免后续曲目连续误报失败；保留播放客户端原有行为。

### 修复

- 保守处理英国、澳大利亚、日本和法国已确认国内手机号范围的前导零；无法安全判断时提示填写完整国际号码。
- 统一任务数据库的关闭逻辑，等待取消事务的异步回滚释放连接，并支持重复及并发关闭，修复 Windows 回归中的数据库清理占用风险。
- 关闭任务数据库时继续等待已退役 driver 连接真正释放句柄；等待超时会明确返回错误，不将仍有连接占用的状态报告为关闭成功。
- 补充跨平台凭据读取、真实子进程回执、选择性恢复、取消及数据库关闭回归测试。
- 发布流程新增 GitHub Actions 两阶段云端构建：Linux 从锁定源码编译 guest，Windows 复验同次运行的提交和文件哈希，再打包、审计并验证实际程序。

### 验证与安装说明

- 自动化回归包含 61 首合成曲目的恢复、授权拒绝及取消场景，并验证已成功文件的内容和身份不变；不使用真实 Apple 账号或下载真实音乐。
- 安装包构建、Linux / Windows CI 及解包审计由发布流程执行，具体结果见对应发布记录。
- 延续现有安装身份与用户数据目录；退出旧版后可覆盖安装，不主动清除设置、会话或已下载文件。
- Windows x64 安装包未签名；成品用户无需额外安装 WSL、Docker 或 FFmpeg。第三方说明见 [NOTICE](NOTICE)。

## 1.0.3 — 2026-11-08

[发布说明](https://github.com/Andrew05213/applemusic-downloader-native/releases/tag/v1.0.3) · [Windows x64 安装包](https://github.com/Andrew05213/applemusic-downloader-native/releases/download/v1.0.3/AppleMusicDownloader-1.0.3-Setup.exe) · [SHA-256 校验文件](https://github.com/Andrew05213/applemusic-downloader-native/releases/download/v1.0.3/AppleMusicDownloader-1.0.3-Setup.exe.sha256)

### 新增与改进

- 原生 Apple 认证交互：显示验证提示、按钮和必要输入，支持重试、取消及超时处理，保留脱敏诊断。
- 手机号登录增加国家区号选择，默认中国大陆 `+86`；已有国际区号不重复添加，邮箱登录保持不变。
- 动态封面改为原生下载，移除 FFmpeg 运行时依赖。
- 更新应用图标和下载按钮图标，图标导航保留悬停提示。
- 窗口宽度为 900px 及以下时，导航栏自动收为 64px 图标栏；拉宽后按原有展开偏好恢复。
- 窄窗口搜索使用独立内容行，支持 Esc 收起，不展开整块侧栏遮挡正文。

### 修复

- 修复窄窗口下导航栏覆盖页面内容的问题。
- 修复设置齿轮 SVG 图形，展开和收起状态均正常显示。
- 整理登录提示的中英文、凭据重试流程和重复取消入口。
- 更新窄屏搜索及侧栏回归测试，覆盖宽窄切换、搜索焦点和内容不重叠。

### 验证与安装说明

- 发布源码的 Linux、Windows CI 均通过：268 项基础测试、144 项 UI 回归、Go race/vet、交叉编译，以及合成音频和动态封面验证。
- 正式安装包通过 283 个文件的解包比对；实际程序验证了启动、原生登录 UI、窄窗口、设置图标、诊断导出及合成 AAC 播放和跳转。
- 自动化测试使用隔离合成数据，未使用真实 Apple 账号；目录、登录和播放可用性仍受账号授权、订阅及地区影响。
- 安装包未签名，Windows 可能显示信誉警告。成品用户无需额外安装 WSL、Docker 或 FFmpeg；开发构建环境要求见[开发指南](docs/development/README.md)。
- 本次升级沿用现有安装身份与用户数据目录，不主动清除设置、会话或已下载文件。第三方材料说明与未完成项见 [NOTICE](NOTICE)。

安装包 SHA-256：

```text
38348c3b8507e3c4aa7f9725f20b8c080116294eb833a565c3ed37bfe979d9f3
```

[发布源码](https://github.com/Andrew05213/applemusic-downloader-native/commit/5660ed99b2927269a01778473204590f46388fa3) · [CI 验证记录](https://github.com/Andrew05213/applemusic-downloader-native/actions/runs/42454707935)
