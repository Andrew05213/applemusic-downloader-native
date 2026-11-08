# 更新日志

本文件记录正式版本的用户可见变化。预发布及更早版本的原始说明见 [Releases](https://github.com/Andrew05213/applemusic-downloader-native/releases)。

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
