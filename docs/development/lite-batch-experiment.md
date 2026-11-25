# Lite 小批量下载实验

下载端锁定 Wrapper [`a8fb29e`](https://github.com/Andrew05213/wrapper/tree/a8fb29e78e078b4d73c07995af62410b1802fd31)，默认仍逐块发送。开发测试可以在启动桌面版之前设置 `AMDL_EXPERIMENTAL_LITE_BATCH=1`。仅全新启动读取该开关；取消变量并重新启动可回到对照组。开关不保存在设定或账号状态里，也不改变音质、选源、登录和播放器路径。

- 下载端先检查 v1 能力，没确认时走原有路径。每个批次最多 4 块、64 KiB，同一流、同一密钥上下文，不跨 MP4 分片；超出单批限制但合法的块仍走原单块接口。
- Gateway 最多允许 3 个批次在途，完成准入和会话校验后释放共享媒体闸门，让播放请求能及时到达 Lite。Lite 每处理一块就释放 SDK 调度锁。批次获发一次性请求许可；Lite 在预检和每块前都通过仅在 Guest 内存在的 Unix socket 检查许可及当前会话，不会额外访问 Apple。
- 排队和会话复验共用独立的 35 秒准入预算。准入完成后才开始计批次执行与回包的 35 秒上限；派发时再次确认剩余验证窗口能覆盖完整执行时间。等待不占媒体闸门，也不延长旧许可；复验超时沿用下载暂停机制，主动取消或注销仍立即撤销许可。
- 无法确认回包、收到不完整前缀或状态冲突时，当前曲目尝试停止并关闭流；不发布部分文件，也不把已发出的批次回退成单块重放。

## 用测试安装包对比

等任务空闲后，完整退出应用即可，不需要注销 Apple 账号。对照组从开始菜单正常打开。测试组在 PowerShell 里用以下命令启动，把路径换成安装目录里的应用 EXE（不是安装器）：

```powershell
$exe = 'E:\你的安装目录\Apple Music Downloader Native.exe'
$env:AMDL_EXPERIMENTAL_LITE_BATCH = '1'
Start-Process -FilePath $exe
Remove-Item Env:AMDL_EXPERIMENTAL_LITE_BATCH
```

变量只传给这次启动。切换组之前都要先退出应用；在已有实例上重新点开快捷方式不会切换。启用后导出的曲目诊断若有 `batchRequests` 大于 0，就确认实际走了批量路径。

## 开发者验证

Windows 上可用新构建的隔离运行时测试开关和拒绝路径，无需账号。运行时及 `TEMP` 建议使用短 ASCII 路径，避免 QEMU 在启动前读不到 BIOS：

```powershell
npm run build:native-runtime -- --out .artifacts/runtime/batch-offline
$env:AMDL_BATCH_TEST_MANIFEST = (Resolve-Path .artifacts/runtime/batch-offline/bundle.json).Path
go -C apple-music-downloader test ./internal/nativeruntime -run '^TestOfflineGuestBatchSwitch$' -count=1 -v
```

性能对比用同一个运行时、同一专辑、同音质和并发数，两组使用各自的输出目录。先记墙钟时间和完整输出，再对比脱敏诊断：`requests` 是逻辑块数，`transportRequests` 是客户端已发起的媒体处理请求数，`batchRequests` 是其中的批次数；`queueMs` 是客户端队列时间，`replyMs` 包含转发、Guest 调度和 Lite 回包。Lite 的 `transport_requests` 记录已开始的单块/批量传输，`sdk_calls` 记录实际的 SDK 调用。批量只能减少请求，不能减少解码块数。队列和回包时间是累计值，不等于任务墙钟时间。

真实账号、专辑和播放尾延迟未验证前，保留默认关闭。离线夹具测得的是接口开销，不代表真实专辑的提速幅度。
