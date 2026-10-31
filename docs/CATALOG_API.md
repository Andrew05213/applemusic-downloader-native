# Catalog API and regional downloads

The HTTP server assembles `JobAPI` and `CatalogAPI` separately. `JobAPI` owns
the durable queue, output files and download settings. `CatalogAPI` owns
`/api/v1/catalog/*` and `/api/v1/browser-download/resolve`; it receives only a
read-only callback for the current download region. Browsing and resolving a URL
do not enqueue jobs.

`CatalogAPIDependencies` supplies the Catalog HTTP client and credential
providers. Catalog browsing and public equivalent-ID lookup use that same
client and in-memory developer-token cache. The cache retains only Apple's
public developer token; account tokens and private responses are not cached.
Public equivalent lookup never requests or sends a Media-User-Token. Personal
Catalog requests retain their account-token and account-storefront checks.

The shared Mode/Kind constants and pure validation rules live in
`utils/ampapi/catalog_domain.go`. HTTP handlers still validate allowed query
keys, duplicate parameters and playlist URL shapes. The API client independently
validates its requests and upstream resources, pagination and credential scope.

## Three regional operations

| Operation | Code boundary | Identity behavior |
| --- | --- | --- |
| Resolve a browser page for download | `resolveEquivalentDownloadURL` in `browser_download.go` | Eligible public songs, albums and music videos use Apple's equivalents API. An album's selected `?i=` track becomes a song. Playlist and station URLs remain unchanged; library URLs are rejected by this endpoint. |
| Apply a job's download storefront | `rewriteDownloadStorefront` in `download_regional.go` | Rewrites only the storefront segment of a validated public URL. It preserves the resource ID and query; library URLs remain unchanged. |
| Find regional metadata and lyrics | `resolveTrackRegionalTextPreferences` in `download_regional.go` | Matches recording identity for text tags and lyrics. It preserves audio identity, manifests, edition/sequence identifiers and output paths. Lyrics fall back to the original recording when the regional lookup fails. |

The first operation prepares a URL for the existing queue submission. The queue
snapshots download preferences when it claims a job, passes them to the isolated
child, and the child applies the second and third operations. These operations
have separate acceptance and identity rules and should not be merged into a
single region-conversion policy.
