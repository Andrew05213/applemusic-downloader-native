# Apple Music Downloader 2.0 private test

Version: 2.0.0-beta.1. For the owner's local testing only. Not a public release.

This build uses standard Electron, a Windows Go service and a bundled Lite/QEMU
guest. It does not install or manage WSL, Docker, remote Apple webviews or a
Widevine browser runtime. The 1.x application, accounts and downloads are not
upgraded, migrated, stopped or removed.

The Wrapper Lite source is pinned to
2fd7e0eaef081f93cf687428b965674937832ba4 and its MIT notice is retained in the
runtime notices. QEMU, Linux, BusyBox and other bundled notices and package
provenance are retained. See NOTICE and the runtime notices for details.

Source-code permission does not grant rights to Android/Apple runtime materials
or other third-party/device material. Redistribution gates remain unresolved:
publicReleaseAllowed=false. Do not publicly upload or redistribute this installer.

Real Apple account login, two-factor authentication, playback/decryption and
media downloads have not been validated by the automated isolated fixtures.
The owner will test these explicitly. Music videos and radio/stations are not
supported in this test build. Full audio playback prepares the AAC file first;
preview fallback is labeled and is not a complete-song success claim.

Sessions and tasks use a separate AppleMusicDownloader-v2-test data directory.
Default media output is Music/Apple Music Downloads 2.0 Test. Uninstall preserves
both. An unsigned test installer may trigger Windows reputation warnings.
