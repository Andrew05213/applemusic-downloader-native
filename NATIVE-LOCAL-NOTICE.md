# Apple Music Downloader Native installation notes

Version: 1.0.5-beta.1. Test build, distributed as a CI artifact, not a published Release. Third-party rights and corresponding-source review remain incomplete.

Original project-authored code and modifications the maintainer has the right
to license are licensed under Apache-2.0; see the included LICENSE and NOTICE.
Imported code and third-party components retain their existing terms. This
project license does not grant rights to material with unresolved authorization.

The application uses standard Electron, native Windows Go services and a bundled
Lite/QEMU runtime. It does not require users to install WSL, Docker, an embedded
Apple website or a Widevine browser. Music videos and radio/stations remain
unsupported. Full playback requires the user's own authorized Music session;
preview fallback is labeled separately from complete-song playback.

The New discovery page replaces the standalone charts entry. It follows the
selected catalog region and shows public editorial picks, releases and songs.

This release includes ordinary M4A export for new ALAC downloads,
consistent bit-depth descriptions, complete audio sample tables, cover/lyrics/tag
preservation and weighted download progress. It does not silently repair old
downloads; choose overwrite or a different directory when downloading them again.

The bundled Wrapper Lite source uses Andrew05213/wrapper (work), pinned to
80dcaa1bc6cc88e41f1d6bb16d0cc9a241d4bca3. Published 1.0.4 binaries
retain their original b2bebc3 pin. It is derived from
WorldObservationLog/wrapper; its upstream MIT notice is retained.
QEMU notice texts and runtime provenance are retained. Linux/BusyBox complete
corresponding-source delivery and other third-party notices remain under review.
See NOTICE and the runtime notices. Source-code licensing does not establish
redistribution authorization for Android/Apple/device or other runtime material.

The maintainer enabled the native build gate, but this does not grant third-party
rights or establish license compliance. The separate legacy WSL policy remains
publicReleaseAllowed=false. See NOTICE and the source repository compliance
record for unresolved source, attribution and redistribution issues.

The predecessor's installation identity and AppleMusicDownloader-v2-test data directory
are deliberately retained to preserve saved sessions, preferences and task/file
bindings. The default media folder's compatibility name remains Apple Music
Downloads 2.0 Test; a previously selected download directory is retained.
The predecessor's legacy 1.x profiles/downloads are not imported or removed.
Uninstall preserves user data and downloaded media.

Automated checks use isolated synthetic profiles, not real user accounts.
This installer is unsigned and may trigger Windows reputation warnings.

### FFmpeg 音频转换工具

Windows 安装包的 `resources/bin/audio-tools/` 包含 Gyan Windows Essentials 9.0.2 的 ffmpeg、ffprobe 及上游 LICENSE、README.txt（GPLv3），不属于本项目 Apache-2.0 许可范围。[构建与归档](https://www.gyan.dev/ffmpeg/builds/) · [对应 FFmpeg 源码](https://github.com/FFmpeg/FFmpeg/commit/946fcce07b)。开发者可运行 `node electron/native-audio-tools.js` 下载并校验固定归档，打包时也会自动执行。运行已安装的客户端无需网络获取这些工具。
