# Apple Music Downloader Native installation notes

Version: 1.0.2. Third-party rights and corresponding-source review remain incomplete.

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
15fe0cb75b866e7460bf8d28d155e813a870e076. It is derived from
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
