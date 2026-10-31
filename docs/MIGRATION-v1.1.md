# v1.1 Migration Guide

> 历史记录：以下内容保留当时的实现、命令与验收语境，不代表当前发布状态。当前指南见[文档导航](README.md)，不要直接用本页旧命令安装或发布 2.0。
> 下文路径基于历史 1.x 源码树；完整旧版请按[维护分支说明](legacy/README.md#维护-1x)独立检出，不能直接在 main 或归档目录执行。

Version 1.1 turns the downloader into one local service shared by the Docker
Web UI, Go CLI, and Windows Electron shell. This guide describes the only
supported migration path; it never requires deleting existing media.

## Preserve local data first

Keep these directories in place before upgrading:

- `downloads/` contains your completed media and is never aged out, moved, or
  renamed by v1.1.
- `data/` contains SQLite job history and wrapper logs.
- `wrapper-data/` contains wrapper login/device state and must be treated as
  sensitive.

The first v1.1 startup indexes pre-existing media as `unmanaged`. It does not
change those files. Deleting job history also does not delete media.

## Docker and Web users

1. Pull the v1.1 source or release bundle while preserving `.env`,
   `downloads/`, `data/`, and `wrapper-data/`.
2. Review `.env.example` and copy only the variables you need into the ignored
   `.env`. Leaving it absent is supported; the capabilities page will show
   unavailable qualities.
3. Start the service with `docker compose up -d --build --wait
   --wait-timeout 180`.
4. Open `http://127.0.0.1:8080`. It is intentionally local-only. Submit a
   six-digit 2FA code through the UI when prompted instead of placing it in a
   URL, environment screenshot, or log.

The old static UI Docker entry points (`Dockerfile.simple` and `Dockerfile.ui`)
are removed. Do not publish wrapper ports `10020` or `20020`; only the local
Web port `127.0.0.1:8080` is supported.

## API integrations

Use `/api/v1/jobs` to create jobs and `/api/v1/jobs/{id}/events` to observe an
existing job. Events support `Last-Event-ID`, so a reconnect must resume the
same job rather than submit another download.

For one compatibility release, `/api/download` and `/api/files*` remain
available as adapters. Their responses contain `Deprecation`, `Sunset`, and
`Link` headers. Migrate to opaque file IDs and the v1 file endpoints now:

The published v1.1 `Sunset` date is **1 July 2027 (UTC)**; the adapters are
removed in v2.0.0.

- `GET /api/v1/jobs/{id}/files`
- `GET /api/v1/files/{fileId}`
- `GET /api/v1/jobs/{id}/archive`
- `DELETE /api/v1/jobs/{id}`

`GET /api/v1/jobs/{id}/archive` downloads only that task's output as a ZIP.
`DELETE /api/v1/jobs/{id}` is available only after a task is terminal; it
permanently deletes that task's registered output files, opaque file IDs, and
task history. It never deletes unmanaged or skipped pre-existing files. Cancel
queued or running work first; deletion of active work returns `409`.

Client-supplied filesystem paths are no longer accepted. The compatibility
routes will be removed in v2.0.0.

## CLI users

Switch scripts to the explicit commands:

```bash
go run . download URL... --quality alac --existing skip --max-retries 2
go run . search album "artist name"
go run . serve
go run . doctor --json
go run . config show --redact
```

`--server`, `--search`, `--aac`, and related legacy flags still map to the new
commands in v1.1 and warn on stderr. They are scheduled for removal in v2.0.0.
Move configuration to `--config FILE` or `AMDL_CONFIG`; a current-directory
`config.yaml` fallback is compatibility-only and warns when selected.

## Existing files and retries

The default `--existing skip` reports a valid prior file as `skipped`. An
invalid pre-existing file is deliberately retained and requires an explicit
`overwrite` policy. Overwrite downloads write and validate a same-directory
temporary file before atomically replacing the original.

Job retry defaults to failed or interrupted tracks. If metadata parsing failed
before a track list existed, retry re-runs that URL. No retry, cancellation, or
restart cleanup removes completed media.
