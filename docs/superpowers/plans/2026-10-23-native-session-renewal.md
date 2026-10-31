# Native session renewal

The latest diagnostics contain successful `music_storefront` / `login_complete`
events, followed by `unverified` without a rejection. The local 90-second
freshness window expires, but status polling only reads the snapshot; the UI
blocks actions before their existing `Ensure` path can revalidate.

- [x] Add deterministic tests for expiry, concurrent polling, cancellation,
  network backoff and actual account rejection.
- [x] Let authorized session polling renew an existing session under the guest
  lifetime context. Keep ordinary snapshots free of network IO and never submit
  passwords.
- [x] Coalesce renewal with operation-boundary validation. Suppress automatic
  status-polling retries after cancellation and use bounded backoff for transient
  failures. Existing `Ensure` operation boundaries may still explicitly restore
  an available session; neither path submits account/password login.
- [x] Verify Windows / Linux authentication and guest tests, plus race checks.

The shared `Ensure` path also had a check/unlock/`Restore` race: a delayed
caller could erase another caller's successful verification or bypass its new
failure backoff. Eligibility and worker registration are now atomic under the
same mutex. An owned renewal returns its final classification exactly once,
before releasing its operation slot; cancellation cannot cause that caller to
start a replacement. Tests reproduce both original races before the fix and
cover 128 generations with 32 simultaneous callers, repeated 40 times under
the race detector on Windows and Linux.

Fresh validation: four related Go packages pass `go vet` and `go test
-mod=readonly -race ... -count=1` on Windows Go 1.25.0 and offline Docker Linux
Go 1.23.1; Windows root `^TestNative` race tests also pass. No production runtime
timeouts or test timing assertions were loosened.

Verification includes a real loopback HTTP fixture: unauthorized requests do
not renew; parallel authorized polls return `checking` without waiting; ending
the HTTP request does not end the guest-owned renewal; cancelling the service
context prevents a late upstream success from publishing authentication. The
two HTTP cases share one parallel 15-second retry wait and a 25-second fixture
lifetime, without a public clock override or real credentials.

No real credentials, installed application changes, packaging or publication.
