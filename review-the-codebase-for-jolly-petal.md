# NZBreX `testing`: deadlock and logic fixes, Go 1.27.2 bump, web server and proxy hardening

## Status of this plan (revised 2026-10-17)

The first draft was checked claim by claim against the working tree by four independent reviews. Almost every bug it lists is real and most line numbers are accurate to ±3. But several proposed fixes would have caused new hangs or panics, some cause-and-effect claims were wrong, and some bugs were missing. This revision fixes all of that. Items marked **(corrected)** changed in substance; **(new)** were missing; **(dropped)** turned out to be wrong.

Done so far (branch `wip/web-job-queue-2025-10`):
- **Phase 0:** rapidyenc C library from upstream, `c136df9`.
- **Phase 1:** Go 1.27.2 bump, `dcce00e`.
- **2.1, 2.2, 2.3:** fixed, with regression tests for 2.1/2.2 (`Session_test.go`); reproduced and verified against local nntp-server (see each item's *Result*).
- **3.13 + 3.10:** band-aid locks replaced in one change, plus the web-server wedge fix from 3.14 (see each item's *Result*).
- **Test setup:** local nntp-server A/B and `tools/nntpseed` are working (see Verification).

Decisions (answered 2026-10-17, see [Decisions](#decisions)): **D1** separate web passwd file; **D2** show per-job settings disabled; **D3** reject broken mode combinations at startup.

## Context

The `testing` branch carries three large in-flight changes:
- a new NNTP proxy (`Proxy.go`, `ProxyHelper.go`, `Server.go`);
- a new web server with an NZB job queue (`webserver.go`, `web/templates/*`, `WEB_JOB_QUEUE.md`);
- uncommitted race-detector band-aid locks in `ConnPool.go`, `Session.go`, `Utils.go`.

The review found two deterministic deadlocks, one unauthenticated remote process kill (`CAPABILITIES` before login), several unbounded-resource paths, and a set of data races, including one that can raise the unrecoverable `fatal error: concurrent map read and map write`. `go vet ./...` and `gofmt -l` are clean and catch none of it. The repo has no `recover()` anywhere and no `_test.go` outside `rapidyenc/`.

**Important for severity:** `main.go` runs the proxy, the web server and NZB processing as **mutually exclusive modes** (`if proxy {…} else if webServerEnabled {…} else if nzbfile`). The proxy has its own session and its own connection pools, so proxy bugs cannot stall NZB download workers. The exception is `NetConn.go`: `readDotLines` is shared, so proxy-motivated changes there must be gated on `proxy`.

Baseline (verified 2026-10-17): the tree builds and vets clean and `go test ./rapidyenc/` passes under go1.27.2 (`GOROOT=/tank0/efs/GO/.bin/go1.27.2`; go1.27.1 also still installed). Nothing in the dependency set blocks the bump.

---

## Phase 0: rapidyenc C library from upstream (DONE, `c136df9`)

- `rapidyenc/rapidyenc/` was a vendored copy of animetosho/rapidyenc, byte-identical to upstream `47f67f5` (2024-08-06). It is now a git submodule (`rapidyenc-c`) pinned to upstream `d223ecc` (v1.1.1-18). That brings in 14 upstream commits, including a scalar encoder overflow fix (sabnzbd/sabctools#209) and an unaligned-load fix in the decoder (nzbgetcom).
- `rapidyenc.h` did not change, so the Go wrapper (our fork of mnightingale/rapidyenc at `2494683`, with local changes) is untouched.
- The stale `.gitmodules` entry for mnightingale/rapidyenc was removed. CI checks out submodules. `clone_rapidyenc.sh` initialises the submodule; before, it was a single commented-out line and did nothing.
- Verified on linux/amd64: `go test -race ./rapidyenc/`, `./NZBreX -testrapidyenc`, `go vet ./...`.
- Leftovers, not touched: `.git/modules/rapidyenc` and `submodule.rapidyenc` in `.git/config`, from the 2025-06-06 submodule attempt. Harmless; remove by hand if wanted.
- The windows and arm64 `.a` files were not rebuilt locally (no cross-compilers installed). CI builds them from source.

To bump later: `git -C rapidyenc/rapidyenc fetch && git -C rapidyenc/rapidyenc checkout <commit> && git add rapidyenc/rapidyenc`, then rebuild with `rapidyenc/build_rapidyenc_linux-amd64.sh`.

---

## Phase 1: Go 1.27.2 bump

| File | Change |
|---|---|
| `go.mod:3` | `go 1.25.3` → `go 1.27.2` |
| `.github/workflows/build_and_publish_test.yml:133` | `go-version: '1.25.3'` → `'1.27.2'` (line moved +2 by the submodule checkout change in `c136df9`) |

`codeql.yml` and the `goreleaser_*.off` workflows use `go-version-file: 'go.mod'`, so no edit is needed. Commit the dependency updates already in the worktree (`gin` and its transitive deps, `testify` 1.10.0 → 1.11.1) in the same change.

---

## Phase 2: Deterministic deadlocks and remote crashes (fix first)

### 2.1 `watchDirThread` deadlock

`watchDirThread` takes `p.mux.RLock()` at `Session.go:642` and calls `refreshDir()` at `:644`. `refreshDir` takes `p.mux.Lock()` at `:601`, and `newSession`/`newssid` lock again at `:440`/`:456`. `LoggedSyncRWMutex` wraps a plain `sync.RWMutex`, which is not reentrant, so this deadlocks on the first tick.

**Trigger (corrected):** `-nzbdir` with any `DirRefresh > 0` (`SetDirRefresh`, `Session.go:405-412`, clamps 1–14 up to 15) and one `.nzb` in the directory.

**Fix:** read `p.refresh` into a local under a short RLock, release, then call `refreshDir()` with no lock held.

**Result (2026-10-17):** fixed. **Correction:** not reachable from the command line today. `DirRefresh` has no flag, and the JSON config loader (`loadConfigFile`, `Utils.go:127-139`) is commented out, so `refresh` is always 0 and `refreshDir` never runs. The deadlock fires on the first tick as soon as the value can be set. `TestWatchDirThreadPicksUpFilesWithoutDeadlock` fails on the old code ("mutex not available") and passes now.

**Note:** even after the fix, `refreshDir` only *registers* sessions. `processorThread` is a TODO stub, so directory NZBs are never processed. See D3.

### 2.2 `CAPABILITIES` before login kills the process

`ps` is built at `Server.go:27-31` without `CliTp`. `CliTp` is only assigned at `:235`, after `AUTHINFO PASS`. The pre-auth switch calls `ps.printCapabilities()` at `:129`, and its `PrintfLine` at `ProxyHelper.go:69` dereferences nil. With no `recover()` in the repo, `printf 'CAPABILITIES\r\n' | nc host 1119` kills the process. `Server.go:302-303` also dereferences `ps.CliTp` inside its own nil guard.

**Result (2026-10-17):** fixed. Reproduced first: `printf 'CAPABILITIES\r\n' | nc` against the unfixed proxy crashed it (panic at `ProxyHelper.go:69` via `Server.go:129`, exit 2). Now it returns the capability list and the proxy stays up. `TestProxyCapabilitiesBeforeAuth` panics on the old code and passes now.

**Fix:** assign `ps.CliTp = cliTp` at construction (the cleanup defer at `:54-58` already handles a non-nil `CliTp`), and fix `:302-303`. Add a `recover()` in `handleConnection` that logs and closes the connection as a safety net, not as the fix. It only covers that goroutine, and a panic after `GetConn` or while `proxyMutex` is held (`:178-247`) would still leak the connection or leave the mutex locked.

### 2.3 Nil mutex panic on a proxied article with `-crc32` (corrected)

The proxy allocates `segmentChanItem` with `mux == nil` at **two** sites: `Server.go:347-351` and `:359-363` (the article-number path). `NetConn.go:889-892` locks `item.mux` with no guard; `:903` has the guard.

A third path also crashes: with `-crc32 -yencwrite` and a cache, `cache.WriteYenc` (`NetConn.go:726/816/881`) locks `item.mux` at `Cache.go:307`, and the yEnc writer goroutine then dereferences `item.s.D`, `item.file.Filename` and `*item.nzbhashname` (`Cache.go:288-290`), all nil for proxy items. That panic is in another goroutine, so no `recover()` in `handleConnection` can catch it.

**Fix:** initialise `mux` at both proxy sites, make the `:889` guard match `:903`, and skip `cache.WriteYenc` when `proxy` is set.

**Result (2026-10-17):** fixed (`WriteYenc` also returns early for items with no session, file or nzb name). Reproduced against server A with the unfixed proxy:
- `-crc32 -yenctest=3`, bad-CRC article: panic at `NetConn.go:890`.
- `-crc32 -yenctest=4 -yencout -cd=…`, good article: panic at `Cache.go:307` via `NetConn.go:881`.

Both now leave the proxy running, and a good article still arrives intact. Findings for Phase 5.2:
- **Mode 4 (default):** a CRC mismatch comes back from rapidyenc as a read error, so it takes the `brokenYenc` path. The client gets `220` with an **empty** article (43 bytes), confirming the silent-truncation bug.
- **(new) Mode 2:** a bad-CRC article is passed to the client as a normal `220`.
- **(new)** After the fix, the bad-CRC case answers `502 Unknown Response: 99932`, leaking an internal code. It should map to a proper NNTP error (e.g. `430`, or try the next provider).

### 2.4 Memory slots never returned when nothing will upload (corrected)

`MemLockWait` runs for every download item (`Workers.go:298`). `MemReturn` happens only on error paths (`Workers.go:312`, `:394`; `NetConn.go:144`; `Cache.go:410`) and after a re-upload (`Workers.go:400`). The comment at `Workers.go:326` saying memory was "already returned in GoDownsRoutine for cache reads" is false (`Routines.go:154-157`).

This is by design: the slot is held from download until upload so that RAM between the two stays bounded. The bug is when no upload follows. After `memMax` downloads (default `2*totalMaxConns`, `Session.go:306-311`), every routine blocks at the bare `<-m.memchan` in `MemLimit.go:112`, which no stop signal can wake, and `waitWorker.Wait()` at `Session.go:382` hangs.

**Trigger (corrected):** a *download* run with no upload providers (`postProviders == 0`, so `allowUp=false`, `Workers.go:711`). The draft's `-checkonly` trigger was wrong: it sets `allowDl=false` (`Workers.go:710`), so nothing is downloaded and nothing is locked. The same leak happens with uploads on for any downloaded item that `pushUP` never queues (e.g. "no provider found", `Workers.go:686`).

**Fix (corrected):** do **not** add a plain `MemReturn` on every 220/920 success; that removes the RAM limit for upload runs. Instead, release the slot on download success only when `!allowUp`, and release it wherever the divider retires an item without queueing an upload. For the blocking receive, see 3.6: `MemLockWait` needs a stop parameter and a rollback.

**Result (2026-10-17):** fixed. **Trigger corrected again:** a run with no upload providers never downloads anything. When `!allowUp`, `pushUP` returns an error and the divider skips `pushDL`, so the `!allowUp` release isn't needed. The real trigger is an item the divider downloads although nothing can upload it: it is missing only on a `NoUpload` provider (or one with errors, no IHAVE/POST, ...).
- Reproduced: B has all 90 articles, A (`NoUpload`) has every second one. The old build downloaded 16 of 45, hit `MEM:16/16`, and hung in `waitWorker.Wait()` until killed.
- **Fix:**
  - The divider now checks `hasUploadTarget(item)` (same rules as `pushUP`) for each checked item that isn't in flight. Items with no target are retired: their slot is returned, and the article is dropped when neither the cache nor `-yencout` needs it. They are not downloaded, and they count toward a new close case `#8` (`dead+done+notarget == todo`).
  - `releaseMemSlots()` returns any slot still held at session end, because `memlim` is global and a leak would shrink it for every later web job.
- **Result:** the run above finishes in 6 s with nothing downloaded.
- **Side effect, a bug fixed (new):** every earlier "normal" A→B run ended through "hit impossible closeCase ... force quit" after about 20 s of waiting. Articles that only A (`NoUpload`) lacked never counted as done. With `#8` the same A→B run takes 10 s instead of 28 s, with identical results (9 downloaded, 9 uploaded).
- **Not fixed (new, noted):** download-only runs (`-cd`/`-yencout` without any upload provider) never download, because `pushUP`'s error skips `pushDL`. The `MemLimit.go` header says that case was intended to work.

### 2.5 `fatal error: concurrent map read and map write` on `s.segmentChans*` (corrected)

Written under `globalmux` in each provider's boot goroutine (`Workers.go:65-81`). Read with no lock from `GoWorker` at `Workers.go:191/247/338` and `Routines.go:31`, which can overlap another group's boot write. That is a real, unrecoverable runtime throw.

**Not racy (dropped from the draft):** `Workers.go:568/670` and `Routines.go:537-538` run after `workerWGconnReady.Wait()`, which comes after every write; `:381` is only reached via divider-pushed items. `len(item.ignoreDlOn)` at `Workers.go:861` is a data race, but `len()` on a map never raises the fatal throw.

**Fix:** build all three maps once before any `GoWorker` starts, and treat them as read-only. Move the `provider.Group = provider.Name` defaulting (`Workers.go:58-60`) ahead of that, or the keys won't match `s.providerGroups` (`Utils.go:191-193`, which holds the raw, possibly empty, groups). Keep exactly one feeder per group.

**Result (2026-10-17):** fixed in the same change as 2.6/3.1. `makeSegmentChans()` builds all three maps and starts one `feedCheckChan` per group before the provider boot loop, then publishes them under `s.mux`. The group default (`Group = Name`) moved into `loadProviderList`.
- **Side effect, a bug fixed (new):** before, all ungrouped providers shared the group `""` in `loadProviderList`'s ACL check. One ungrouped download-only provider plus one ungrouped upload provider made the old build exit at start ("set 'NoUpload' to same value in group ''"). Verified with the 3.13 build, which exits 1; the new build runs the session.
- **Side effect in the proxy (new):** the proxy never ran `GoBootWorkers`, so its providers kept `Group == ""`. After the first provider answered 430, `checkedProviderGroups[""]` made the proxy skip every other ungrouped provider. Verified: with providers [B, A] and an article missing only on B, the 3.13 proxy returns `430 NOPG` to `STAT`, and the new one returns `223`.

### 2.6 `close of closed channel` on session teardown (corrected; do together with 3.1)

- `StopRoutines` (`Routines.go:537-538`) closes `segmentChansDowns`/`Reups` via `closeSegmentChannel`, and `closeSession` (`Session.go:484-495`) closes them again. Only reached when `len(sessMap) <= SessThreshold`; `SessThreshold` is JSON-config only and defaults to 0, which takes the `removeSession` branch.
- `segmentChansCheck[group]` is closed by the feeder (`Workers.go:102`) and again in `closeSession` (`Session.go:485`).
- `close(s.stopChan)` (`Routines.go:541`) on a second launch: **latent**, because no current caller relaunches a session. The proxy and web sessions (`main.go:98`, `:122`) have a **nil** `stopChan`, so any `StopRoutines` on them would panic.

**Fix:** one owner per channel, keyed by **group**, not provider. Guard with `sync.Once`. Create `stopChan` once in `LaunchSession` before any watcher starts. **Do not** recreate it inside `closeSession`: `WatchOpenConnsThread` re-reads the `c.s.stopChan` field every loop (`ConnPool.go:643`), so swapping it races, and a watcher that misses the close spins forever on its `default` branch.

**Result (2026-10-17):** fixed together with 3.1 and 2.5.
- `s.stop()` closes `stopChan` once (`sync.Once`), and ignores a nil channel. The proxy and web sessions in `main.go` now get a `stopChan`. Sessions are single-use: `LaunchSession` rejects one that has already been stopped (every caller creates a new session anyway).
- `closeWorkChans()` closes the downs/reups channels once, iterating the per-group maps, not the providers. The check channel belongs to its feeder. `closeSession` no longer closes anything; it only drops the maps.
- Tests: `TestSessionTeardownClosesEachChannelOnce` (two providers in one group, `StopRoutines` twice, then `closeSession`, then a relaunch is rejected) and `TestStopRoutinesWithoutChannels` (proxy-style session, nil channels).

---

## Phase 3: Hangs and resource exhaustion in the engine

**Stop-channel design (settled 2026-10-17).** 3.2, 3.6, 3.7 and 2.6 depend on it.
- **Session lifetime** (workers, divider, feeders): `s.stopChan`. It is created with the session (`newSession`, and in `main.go` for proxy/web) and closed only by `s.stop()` (`sync.Once`). Nobody sends on it, and it is never replaced, so readers may copy it into a local once. Sessions are single-use.
- **Pool lifetime** (`WatchOpenConnsThread`, `Speedmeter`): a stop channel per `ConnPool`, closed once by `KillConnPool`. Pools are built per session but the proxy's pools are never killed, so they must not hang off the session's channel (to do in 3.7).
- **Global `memlim`**: `MemLockWait` takes the caller's session stop channel as a parameter and reports an abort (3.6). The limiter itself has no stop.
- **Segment channels**: one close owner each. The feeder owns the check channel; `closeWorkChans` (via `StopRoutines`, after the divider has quit) owns downs/reups. Nobody else closes them.

### 3.1 `closeSegmentChannel` leaves the channel open (corrected)

`Routines.go:547-569`: if the channel holds a buffered item, it is received, logged as "LOST", and the channel is not closed. Impact is smaller than the draft said. `StopRoutines` runs after the divider ends, which needs `dlQ`/`upQ` at 0, so the channel is normally empty. Download/re-upload routines also have 15 s timeouts once `segcheckdone` is set (`Workers.go:270-281`, `361-372`).

**Fix:** drain, then close **once per group**. `StopRoutines` loops over providers, not groups, so with several providers per group a plain "close unconditionally" panics on the second call, and `closeSession` closes again. This must land in the same change as 2.6.

**Result (2026-10-17):** fixed with 2.6. The owner closes the channel without draining it first: buffered items stay receivable, so the workers process them before they see the close, instead of the old code taking one item off and logging it as "LOST". A non-empty channel at close is logged.
- Verified under `-race` against the local servers: A→B, two providers in one group (`local-b1`/`local-b2`, which split the 9 uploads 5/4), and ungrouped providers. In every run each group's channels closed once, with no races and no panics.
- Web mode with two queued jobs: both complete. The 27 race reports are all in `webserver.go` job fields (4.2).

### 3.2 Check feeder busy-wait (corrected)

`Workers.go:84-103` spins until the channel is empty, then closes. If every worker failed to get a connection at boot, it spins forever. A second hang cause: `GoWorkDivider` keeps looping while `checked != todo` (`Workers.go:1029`). Selecting on `s.stopChan` alone does not help, because nothing closes `stopChan` until the divider has ended.

**Fix:** give the feeder and the divider a shared "give up" signal, for example closed when every check routine of a group has exited, plus a deadline. The feeder's close must follow the 2.6 single-owner rule.

**Result (2026-10-18):** fixed. Testing found four more hangs on the same path; all are fixed here.
- **Give-up signal.**
  - `checkerGroup` counts each group's check routines, built with the channels in `makeSegmentChans`.
  - The count drops when a check routine exits and for every routine that never starts (boot `GetConn` failure, shared-conn failure). At 0 it closes `gone`.
  - The feeder selects on `gone` and closes its channel (still the only owner).
  - When every group is gone, `checked != todo` and nothing is in flight, the divider quits with `checkersGone`.
- **(new) A failed check was dropped.** A `STAT` with no reply, or no connection, never reached `checkedOn`, so `checked == todo` never held.
  - `checkItem` retries once on a broken connection.
  - Otherwise `markCheckFailed` sets `errorOn`/`ignoreDlOn` for the group (not `missingOn`), and the divider retires the item ("no upload target").
  - A check routine quits after 3 items in a row with no connection.
- **(new) The watchdog only ran with `-verbose`** (it lived in the stats block; `-verbose` is on by default) and was wrong.
  - It counted down whenever `segm_perc` stayed below 100, so any NZB with segments missing everywhere was killed about 20 s into the transfer: 32 of 40 uploads in the test. Without `-verbose` there was no watchdog, so a dead group hung forever.
  - Replaced by an always-on 3-minute no-progress check over the counters plus `s.workDone` (every finished attempt, so slow failing work doesn't count as stuck).
- **(new) Conn failures left items "in flight" forever.** `GoReupsRoutine`/`GoDownsRoutine` returned on a `GetConn` failure without `FlagError`, so `flaginUP`/`flaginDL` stayed set and the divider waited forever.
- **(new) Failed uploads were retried forever.** `matchThisUP`/`pushUP` ignore `fails`. After `maxItemFails` (3), `FlagError` now sets `errorOn` for that provider. The retry delay works now: `.Unix()` instead of `.Second()`, and `retryOn` is set.
- **(new) Mem-slot deadlock on upload errors.** The re-upload loop returned the slot on error. Downloads took every slot, and the re-queued item waited in `MemLockWait` behind items that held them; goroutine dump: all 8 routines in `MemLimit.go:106`. A failed upload now keeps its slot until it succeeds or is retired; `releaseMemSlots` returns the rest at session end.
- **(new) Work after the stop.** The downs/reups routines drop items still queued once the session has stopped, and `GetConn` no longer dials after the stop.
- **(new) Provider down for the session.** When `GetConn` gives up on a dial error (`errDial`, now wrapped through `connect`/`NewConn`), `Provider.markDown` takes the provider out for the rest of the session.
  - The routines and `pushUP`/`pushDL`/`hasUploadTarget` then skip it without touching the network.
  - Never in proxy sessions, whose pools live as long as the process. Checked: B dialled again on every request, never marked down.
  - Without this, each item cost 3 × ~9 s of dial retries: 90 uploads ran past 400 s.
- Tests: `TestFeederClosesWhenCheckersGone`.
- **Verified under `-race` against the local servers, no races in any run:**

| Scenario | Before | After |
|---|---|---|
| B down at boot, no `-verbose` | hangs (killed at 120 s) | 13 s |
| Segments missing everywhere, slow uploads, `-verbose` | killed at ~20 s, 32/40 uploads | 50 s, 40/40 |
| B killed during checks | — | 31 s (first fix round: 385 s via watchdog, then 188 s) |
| B killed during uploads | — | 32 s, close case #8, no slots leaked |

  - A→B, same-group and the 2.4 leak setup: unchanged results.
  - Web mode: both jobs complete; the 25 races are all in `webserver.go` (4.2).
- **Note:** `UseNoDeadline = true` (`Config.go`): conns have no read deadline, so a server that accepts and then goes silent can still block a routine, and with it `waitWorker`, after the watchdog has fired. That belongs with 3.14 (`checkCapabilities`).
- **Limitation:** items not checked on a dead group are given up for the whole session, even when other groups could still serve them.

### 3.3 Shared-connection early returns (dropped as a bug)

`SharedConnGet` only returns an item with `conn != nil`. On the error path it has already put `nil` back into `sharedCC` (`ConnPool.go:749`). The `conn != nil && srvtp == nil` window doesn't occur: `CloseConn` runs in the holder's own goroutine. The guards at `Routines.go:45-47`, `:170-172`, `:348-350` are defensive dead code. If they are touched, never push a token back on the error path; the cap-1 channel is already refilled.

### 3.4 `CloseConn` is not nil-safe or idempotent (corrected)

- **Nil:** `ConnPool.go:520` dereferences `connitem` without a check. `:362` is unreachable (`ParkConn` dereferences first), but `:782`/`:794` (`ReturnSharedConnToPool*`) can pass nil. Add a nil guard.
- **Double close:** `readDotLines` closes the connection itself (`NetConn.go:376`, `:486`, `:576`; `:337` is unreachable, `:476` doesn't close) and the caller closes again (`Routines.go:179`). `openConns--` (`ConnPool.go:540`) runs twice, so the count drifts negative. **The consequence is the reverse of the draft:** `KillConnPool` loops while `openConns > 0`, so negative drift makes it *exit early* and leave real connections open; `GetConn` can dial past `MaxConns`.
- **Fix (corrected):** guard **only the decrement** (`if connitem.conn == nil` → skip `openConns--`), and **always** send the `nil` token to `sharedCC`. In the double-close case the second call is the only one that sends it. An early `return` at the top would leave sibling routines blocked at `ConnPool.go:729`.
- The draft's "blocking send under `provider.mux`" is **dropped**: `checkCapabilities` passes `nil` for `sharedCC` (`NetConn.go:989`). A non-blocking `select` there is harmless.

**Result (2026-10-18):** fixed as planned. Only the close and the decrement are guarded (`closeConnOnce`, which runs only while `connitem.conn != nil`). The nil token is always sent, and a nil item no longer panics; `GetConn` did hit this, via `go c.CloseConn(nil, nil)` for a nil item taken from the pool. Test: `TestCloseConnNilSafeAndDecrementsOnce` (panics on the old code).

### 3.5 `KillConnPool` waits unboundedly while holding `PoolsLock` (corrected)

`ConnPool.go:144-178` holds the global `PoolsLock` across an unbounded `Sleep(10ms)` loop until `openConns == 0`. It spins only on a *positive* leak (a connection that is never returned), not on the negative drift from 3.4. `LaunchSession` calls it per provider (`Session.go:386-388`).

**Fix:** drain under a short lock, wait outside `PoolsLock` with a deadline. Force-close only items that are idle in the pool; closing a `connitem` another goroutine is using is a data race.

**Result (2026-10-18):** fixed together with 3.11 and 3.12.
- `ConnPools` and `PoolsLock` are deleted (never read), which also removes the lock-order inversion.
- `KillConnPool` closes the idle conns and waits up to `killConnPoolWait` (5 s) for the ones in use, without a global lock. It never closes a conn another goroutine holds, and it logs a WARN if it gives up.
- No WARN in any verification run: after `waitWorker`/`waitPool` nothing is in use anymore.

### 3.6 Blocking receives with no stop case (corrected)

`ConnPool.go:435` (`<-c.pool`, "infinite wait"), `ConnPool.go:729` (`<-sharedCC`), `MemLimit.go:112` (`<-m.memchan`). `:436` dereferences `connitem.connid` without a nil check (latent).

**Not mechanical:**
- `memlim` is global across sessions and `MemLockWait` returns nothing. A stop case needs a signature change (return ok/err), and on abort it must undo `m.waiting` and `delete(m.memdata, item)`. Otherwise the next `MemLockWait` on that item hits `os.Exit(1)` (`MemLimit.go:88-90`). Callers must skip `GoDownsRoutine` after an abort.
- `StopRoutines` closes `stopChan` *before* `waitWorker.Wait()`, so running workers will start getting errors from `GetConn`. That's acceptable, but expect the log noise.
- Read `stopChan` once into a local (see the Phase 3 note).

**Result (2026-10-17):** fixed.
- `MemLockWait(item, who, stop) bool` takes a free slot first, otherwise it waits for a slot or `stop`. On abort it rolls back `waiting`/`memdata` and returns false. `TOTAL_MemLock` now only counts successful locks; aborts go to `TOTAL_MemLockAborted`.
  - The download routine gives the item back (clears `flaginDL`, decreases the queue count) and skips `GoDownsRoutine`.
  - `GoReupsRoutine` returns an error wrapping `errSessionStopped`, and its caller then skips `MemReturn`.
- `GetConn`'s infinite wait and `SharedConnGet` select on the pool's `sessStop` (the owning session's `stopChan`, captured in `NewConnPool`). `SharedConnGet` prefers a waiting token over a closed stop. A nil item from the pool is no longer handed out.
- **Bug fixed (new):** `SharedConnGet` called `ExtendConn(connitem)` on the still-nil named return, so reused shared connections never had their deadline extended. It now extends `aconnitem`.
- Test: `TestMemLockWaitAbortsOnStopAndRollsBack`.
- Verified by stopping server B mid-upload. The session returns after 63 s with no races: 4 re-upload waits aborted, 2 leaked slots were returned at session end. That needed 3.15 as well.

### 3.7 Goroutine leaks (corrected)

| Site | Status |
|---|---|
| `Workers.go:199-207`, `:255-263`, `:346-354`: three ticker goroutines per worker, never stopped | confirmed |
| `ConnPool.go:568-660` `WatchOpenConnsThread`: no exit path at all | confirmed |
| `ConnPool.go:828` `Speedmeter`: never stopped (only runs when `!CheckOnly && PrintStats>0`) | confirmed |
| `NetConn.go:476`, `:487` read errors with `YencTest` 3/4: async decoder blocks on `<-decodeBodyChan`, or `pipeWriter` is never closed | confirmed; **`:578` does not leak** (`CloseWithError` at `:575` unblocks it) |
| `Cache.go:311-324` `WriteYenc` spins if `GoYencWriter` was never started | latent: CLI `-checkonly` never downloads, and the web per-job override that could reach it is commented out |

**Fix:** stop channel per `ConnPool`, closed by `KillConnPool`; `time.Ticker` with `defer Stop()`; move the `NetConn` cleanup into a `defer` that uses a "done" flag, so the success path (`:669-684`) doesn't close `decodeBodyChan`/`pipeWriter` twice.

**Result (2026-10-18):** fixed, except the `WriteYenc` spin (latent, unchanged).
- **Tickers:** the three per-routine tick goroutines are a `time.Ticker` with `defer Stop()`.
- **Pool goroutines:** `ConnPool.poolStop` is closed once by `KillConnPool`. `WatchOpenConnsThread` and the per-pool `Speedmeter` select on it; the global `Speedmeter` (nil `cp`) keeps running.
- **`readDotLines`:** a deferred cleanup with a `finished` flag closes the pipe (`CloseWithError`) or `decodeBodyChan` on every early return, then waits for the decoder goroutine.
- **(new) Worse than the table said** for `-yenctest=4`: the deferred `ReleaseDecoder` gave the decoder back to the pool while the leaked reader goroutine still read from it (use after release). The reader now signals `done` as its last action, and the cleanup waits for it before the release runs.
- **(new) `-yenctest=3`:** after a decode error the decoder goroutine stopped reading (`break`). If `decodeBodyChan` (6666 slots) was full at that moment, the reader blocked forever on the send. It now keeps draining (the `broken` flag skips the work).
- Test: `TestReadDotLinesEarlyReturnStopsDecoder` (fails on the old code).
- **Verified:** 4 sequential web jobs, goroutine dump 20 s after the last one. Old build: 122 goroutines (49 leaked worker tickers, plus watchers and speed meters of finished sessions). New: 10, and the 4 pool goroutines among them belong to the web processor's long-lived base session.
- **Note for 6.2:** `LaunchSession` replaces the global `core_chan`/`async_core_chan` when `YencCpu`/`YencAsyncCpu` changes, under `globalmux`, while `get/returnAsyncCoreLimiter` use them without it. Only reachable if per-job settings change those values.

### 3.8 `MemLimiter` check-then-act gap (corrected; fix added)

`MemLimit.go:74-90`: `memdata[item]` is set before the blocking wait, `memlocked` only after it at `:113`. No concrete path with two concurrent lockers on one item was found. A double `MemReturn` hits `log.Fatalf` (`:148`) only if the channel is full; otherwise it silently adds an extra slot and drives `memlocked` to −1. `MemAvail()` (`:67`) tests `m.waiting < m.mem_max`, which reports "available" with an empty `memchan`; it is used at `Workers.go:523`.

**Fix:** set `memdata` and `memlocked` together, under one lock, after the slot is acquired. Make a second `MemReturn` a logged no-op instead of a fatal error. Base `MemAvail` on `len(m.memchan)`.

**Result (2026-10-18):** fixed, except `MemAvail` (kept on purpose, see below).
- `memdata` is now the only record of who owns a slot. It changes together with `item.memlocked` (now 0/1), both under `m.mux`, and only after the slot was taken. Lock order is `m.mux` → `item.mux`; no caller holds `item.mux` when calling `MemLockWait`/`MemReturn`.
- `MemReturn` checks and removes ownership in one critical section. A second return (or one for an item without a slot) is a logged no-op; the `log.Fatalf` on a full chan is now a logged `BUG`.
- A second `MemLockWait` on an item still waiting (was `os.Exit(1)`) now waits for the first one and shares its slot.
- **`MemAvail` unchanged in meaning.** Gating `pushDL` on free slots made a `-mem 4` run 25% slower (38 s → 50 s, twice, both orders): each freed slot waited for the next divider pass before a download was queued for it. The old test ("fewer than `mem_max` callers waiting") only bounds how many downloads wait in `MemLockWait`, and they hold no memory. `waiting` is now an `atomic.Int64`, so `MemAvail` takes no lock: `pushDL` calls it while holding `item.mux`, which would otherwise invert the new lock order.
- Tests: `TestMemReturnTwiceReturnsOneSlot` (old code: `log.Fatalf` "chan is full") and `TestMemLockWaitSameItemTwiceSharesOneSlot` (old code: `os.Exit(1)`).
- Verified with `-race` against the local servers: A→B, same group, slot leak, `-mem 4` (39 s, old build 38 s), kill B during uploads (ends in 32 s). No races, no "non-memlocked" warnings.
- **Noted:** with `-mem 4` and 4 connections per server, uploads run at about 2 articles/s (~65 KiB/s) to the local server on both builds. This is the race build, but it looks slow; worth a look with a normal build later.

### 3.9 `s.preBoot`/`s.active` guarded by two mutexes (corrected; fix added)

`Session.go:170-178`/`:520-524` use `s.mux`; `Workers.go:13-25` uses `globalmux`. `LaunchSession` and `GoBootWorkers` run in the same goroutine (`Session.go:369`), so they don't race each other. The real race is `webserver.go:854` reading `active` under `globalmux` while `closeSession` writes it under `s.mux`. The nil dereference at `Workers.go:15` and the early returns before the `defer` at `:27` are unreachable (`LaunchSession` rejects a double launch at `:171`).

**Fix:** guard both fields only with `s.mux` (or make them `atomic.Bool`), and read them that way in `webserver.go`. Move `defer waitDivider.Done()` above the early returns anyway; it costs nothing.

**Result (2026-10-18):** fixed. `GoBootWorkers` sets both fields under `s.mux` (no more `globalmux`), `defer waitDivider.Done()` is first, and the `s == nil` branch no longer dereferences `s` in its log line. `collectStats` reads `active` together with `providerList` under one `session.mux.RLock()`. Lock order there stays `globalmux` → `webProcessor.mux` → `session.mux`; nothing takes `globalmux` while holding a session's `mux`. The race detector never caught this one in the web runs (the write in `closeSession` and the poll never overlapped), so the check is: `-race` CLI A→B clean, web race run with both jobs completed, and the only 13 remaining reports are the job-field races in `webserver.go` (4.2).

### 3.10 `WaitGroup.Add` after `Wait` has started (corrected) and a boot hang (new)

`NewConnPool` (via `loadProviderList`, `Session.go:290`) spawns `Speedmeter` and `WatchOpenConnsThread`, which call `workerWGconnReady.Wait()` (`ConnPool.go:572`, `:802`) before the `Add(1)` loop at `Session.go:356-366`. `Wait()` can return early, which is harmless. **Dropped:** the "WaitGroup is reused" panic; it needs reuse after reaching 0, and this WaitGroup is local and never reused. At most `-race` reports it.

**New:** `Workers.go:49-52` skips providers with `MaxConns <= 0` (`continue`) **without** `workerWGconnReady.Done()`, while `Session.go:358-360` calls `Add(1)` for every provider. `Workers.go:158` then waits forever. `loadProviderList` doesn't filter these providers out.

**Fix:** `Add()` before any pool is built; call `Done()` (or skip the `Add`) for providers that are skipped.

**Result (2026-10-17):** fixed together with 3.13 (reverting the `globalmux` band-aids made the race detector report this `Add`/`Wait` race, `ConnPool.go:137` vs `LaunchSession`).
- `loadProviderList` now calls `Add(1)` right before each `NewConnPool`, and skips providers with `MaxConns <= 0`.
- Proxy/web sessions, which never boot workers, release it in `LaunchSession`. `GoBootWorkers` calls `Done()` on its skip path. The error path releases what it added.
- Verified: with a `MaxConns: 0` provider the old binary hangs at boot (killed by timeout), the new one skips it and completes. Proxy mode still starts its pool watchers. The race report is gone.

### 3.11 `ConnPools` map (corrected)

Keyed by a per-session index (`Utils.go:200`, `ConnPool.go:134/181`), so sessions overwrite each other. Harmless: nothing ever reads the map. **Fix:** delete it.

**Result (2026-10-18):** deleted, with 3.5.

### 3.12 `PoolsLock` ↔ `provider.mux` lock-order inversion (latent)

`NewConnPool` takes `provider.mux` (`ConnPool.go:88`) then `PoolsLock` (`:133`); `KillConnPool` takes them in the reverse order (`:144`, `:183`). Not live today, because each session copies its providers (`Utils.go:197-198`). Fix while the file is open: never hold `provider.mux` while taking `PoolsLock`.

**Result (2026-10-18):** gone with `PoolsLock` (3.5/3.11).

### 3.13 The uncommitted band-aid locks (corrected; must land with their replacement)

- `ConnPool.go:569-571`: `globalmux` around `start := time.Now()`. Protects nothing. **Revert.**
- **(new)** `Session.go:354/368`: `globalmux` around `.Add()` on local WaitGroups. Protects nothing. **Revert.**
- `Utils.go:149-150`: `s.mux` spans all of `loadProviderList`, including `NewConnPool`. No deadlock path was found, but narrow it. The lock exists because `extractSessionStats` (`webserver.go:771-780`) reads `s.providerList`/`s.segmentList` under `s.mux.RLock`, so keep the publish under `s.mux`, and move `s.providerList = nil` (`Session.go:289`, unlocked) under it too. `s.mux` is per session and never protected the shared `cfg.providers` unmarshal against concurrent web jobs; that needs a global lock.
- `Session.go:271-273`: build `segmentList` locally, assign it once under `s.mux`, and use the local slice at `:278-283` (`len()` there is read without the lock).
- `Utils.go:208`: removing `time.Sleep(3s)` was right; nothing depends on it.

**Ordering:** a plain revert reintroduces the races with `extractSessionStats`. Revert and replace in one change.

**Result (2026-10-17):** done, together with 3.10.
- Both no-op `globalmux` band-aids reverted.
- `loadProviderList` parses into a local slice (the shared `cfg.providers` field is removed; nothing else used it), builds the provider list and groups locally, and publishes both once under `s.mux`. `LaunchSession` builds `segmentList` locally and publishes it once under `s.mux`.
- `extractSessionStats` checks `segmentList` under the lock; `collectStats` copies `providerList` under `session.mux`.
- Verified with `-race`, CLI download A → B: clean before and after.
- Verified with `-race`, web mode (2 jobs, `/api/jobs` + `/api/stats` polled every 50 ms): the old build logged 29 races including `Session.go:272` vs `webserver.go:768`, then wedged (below). The new build completes both jobs. The remaining 20–26 races are all inside `webserver.go` job-field handling (4.2).

### 3.15 (new) `CMD_IHAVE` leaves the textproto pipeline stuck: the connection hangs forever

`NetConn.go` `CMD_IHAVE` returned on any `ReadCodeLine(335)` error **before** `EndResponse(id)`. `ReadCodeLine` returns an error for any reply other than 335, including normal `435`/`436`. The caller keeps the connection for `436` (retry) and `435`/unknown (unwanted). The next command on it then blocks forever in `StartResponse`, before any I/O, so no read deadline helps.

**Found** by the 3.1/3.2 check "stop server B mid-run". nntp-server answers `436 Retry later` while shutting down, and the session hung in `waitWorker.Wait()`. The goroutine dump showed 4 re-upload routines in `textproto.(*sequencer).Start` via `NetConn.go:168`. A live provider answering 435/436 during a normal run wedges that connection the same way.

**Fix:** `EndResponse` right after `ReadCodeLine`. A read error with no reply line still returns early; 435/436 now reach the existing switch. Every other `StartResponse` in the code already ends on all paths. **Note:** `textproto.Cmd` returns an `id` even when the write fails, so a conn whose `Cmd` failed must never be reused. The callers close it on `code == 0`.

**Result (2026-10-17):** fixed. `TestIHAVEReplyNot335KeepsConnUsable` (fake server answers 436, then 435) blocks on the old code and passes now.

**Also noted (fixed with 3.2):** `FlagError` (`Workers.go`) set `retryIn = int64(time.Now().Add(5 * time.Second).Second())`. That is the seconds field of the current minute (0–59), not a Unix time, so `retryIn > now` was never true and the 5 s retry delay never applied (it also never set `retryOn`).

### 3.14 Remaining unsynchronised accesses (corrected)

Still races:
- `Session.go:467`: `len(s.proc.sessMap)` read without `p.mux`.
- **(new, fixed 2026-10-17) Web server wedge.** Reproduced in the web race test. The nil-`ConnPool` panic below fired in `collectStats` while it held `webProcessor.mux.RLock()` without `defer`. Gin's recovery caught the panic, so the read lock leaked: `removeSession` then blocked forever on `p.mux.Lock()`, the next job never started, and every `/api/jobs` and `/api/stats` request hung. **Fix:** the session loop runs in a closure with a deferred unlock. Each provider's `ConnPool`, article counters and speeds are copied under one `prov.mux.RLock()`, and a nil pool is skipped. This also removed the `GoCheckRoutine` (`Routines.go:78-79`) vs `collectStats` counter races.
- **`ConnPool.go:184` (worse than the draft said).** `provider.ConnPool = nil` runs in `KillConnPool` (`Session.go:387`) before `clearSession` (`:401`). Meanwhile `webserver.go:854-856` calls `prov.ConnPool.GetStats()` while `session.active` is still true. That is a reachable nil-pointer panic from a web poll.
- `NetConn.go:987-989`: `checkCapabilities` holds `provider.mux` across two network reads with no deadline (`UseNoDeadline` makes `ExtendConn` a no-op). Add a deadline; don't hold the lock across I/O.

**Not races (dropped):**
- `Workers.go:129`, `Routines.go:355-368`, `Workers.go:616-618`: written in the boot goroutine before the workers start, so the writes happen first.
- `writeCsvFile` (`Utils.go:444-451`): runs after the workers are joined.
- `Session.go:295/358/386`: run on the writer's own goroutine.

---

## Phase 4: Web server correctness

### 4.1 `StartWebServer` can't report a bind failure

`webserver.go:535-549` runs `router.Run`/`RunTLS` in a goroutine, logs the error at `:545`, and returns `nil`. The only error it can return is a template parse failure (`:497`), which embedded templates make near-impossible. So the `os.Exit(1)` at `main.go:131-134` never fires for a busy port, a bad address or a missing TLS cert (defaults to `fullchain.pem` relative to CWD, `Flags.go:97-98`). The process then sits in `wg.Wait()` with no web server.

**Fix:** `net.Listen` synchronously and return its error; `tls.LoadX509KeyPair` up front when `-webssl`; then `go srv.Serve(ln)`.

### 4.2 Shared job state written under a read lock

`ListJobs` (`webserver.go:178-218`, writes at `:194-200`) and `collectStats` (writes at `:926-933` under `jobQueue.mu.RLock` from `:897`) both modify `*QueuedJob` under `RLock`. `ListJobs` returns live pointers that are marshalled after the lock is dropped (`:652-655`), while the completion goroutine writes `Status`/`Error`/`FinishedAt` (`:293-302`).

**(new)** `GetJob` also returns a live pointer: `handleJobUpdateConfig` reads `job.Status` at `:698` without the lock, then writes the config (check-then-act). `extractSessionStats` checks `s.segmentList == nil` at `:768` before taking `s.mux`, which races with `Session.go:272`.

**Fix:** readers must not modify jobs. The job updates its own progress (it currently advances only when someone polls, so it stays 0 with no browser open, even after completion). Return copies (`[]QueuedJob`) under the lock.

### 4.3 Locks held across long work

- `AddJob` (`:117-147`) holds `jq.mu` while it opens, gunzips and parses the whole NZB (`Utils.go:67-124`). It swallows the parse error (`err == nil &&`), so a corrupt file is accepted with HTTP 200. The parse is then redone by `LaunchSession` (`Session.go:211`).
- `ListJobs` holds `jq.mu` **and** `webProcessor.mux.RLock` across `extractSessionStats`, which walks every segment taking a per-segment lock (`:767-806`). `newSession`/`newssid` need `p.mux.Lock`, so starting a job waits behind the stats poller.
- `collectStats` holds `globalmux.RLock()` for its whole body (`:847-848`). `globalmux` writers (`Workers.go:13`, `Session.go:301/354`, the `ConnPool.go:830` Speedmeter tick) block behind it, and once a writer is waiting, new readers block too.

**Fix:** parse outside the lock and reject parse errors with 400. Keep running counters on the session (atomics updated by the workers) and have the web layer read those instead of scanning.

### 4.4 Login, CSRF, timeouts, uploads (corrected; needs D1)

- **No authentication** on any route (`:512-525`). The default bind is `127.0.0.1:11911`, but `WEB_JOB_QUEUE.md:115-118` tells users to expose it on `:443`. Delete that example.
- **Login (corrected):** the draft's "reuse `loadPasswdFile`/`verifyPassword` from `ProxyHelper.go`" does not fit as written. Those functions write and read the **global** `passwdMap` behind `proxyMutex` (which also guards `ProxySessions`/`CountConns`). A separate web file would overwrite the proxy users, and reusing `.proxypasswd` would give every proxy user full web control (see D1). They also inherit the 5.2 bugs (map wiped in place, timing oracle). The file format is proxy-specific (username ≥ 10 chars, maxconns/expiry fields, and `verifyPassword` never checks expiry). And bcrypt plus a random sleep of up to 128 ms on every request is too slow for pages that poll every 3–5 s.
  **Fix:** refactor into `parsePasswdFile(path) (map[string]*UserData, error)` plus a verify function that takes the map (fixing 5.2 at the same time); give the web server its own credential map; cache successful verifications briefly.
- **CSRF (corrected):** `POST /upload`, `POST /api/jobs/start-all` **and `POST /api/jobs/:id/start`** (empty body, guessable `job-N` IDs) are simple cross-site requests. Basic auth does **not** stop CSRF, because browsers send cached credentials cross-site. Require a custom header (forces a preflight; update the `fetch` calls in `index.html:118` and `jobs.html`) and check `Origin`/`Sec-Fetch-Site`. For DNS rebinding, add a `Host` allowlist.
- **Timeouts:** `router.Run` uses a zero-value `http.Server`. Set `ReadHeaderTimeout: 10s`, `IdleTimeout: 60s`. Limit the body with `http.MaxBytesReader` rather than `WriteTimeout`, so slow uploads still work.
- **Upload size:** only gin's default `MaxMultipartMemory` (32 MiB in memory); the rest streams to an unbounded temp file. Add a size limit.
- **Upload directory (worse than the draft said):** `filepath.Join(os.TempDir(), "nzbrex-uploads")` with `0755` (`:584-596`). Gin's `SaveUploadedFile` also calls `os.Chmod(dir, 0750)` and `os.Create(dst)`, both of which follow a planted symlink, for the directory or the file. Use `os.MkdirTemp` once at startup (mode 0700) and write the upload yourself with `O_EXCL|O_NOFOLLOW`.
- **Filename collisions:** `dst := filepath.Join(uploadDir, file.Filename)` (`:592`). Two uploads of `foo.nzb` overwrite each other, including one that is running. Use the job ID as the on-disk name.
- **Cleanup:** there is no `os.Remove` anywhere in `webserver.go`. Delete uploads on job deletion/completion.
- **TLS:** set `MinVersion: tls.VersionTLS12` explicitly.
- `gin.Default()` trusts all proxies (`0.0.0.0/0`): call `router.SetTrustedProxies(nil)`.
- Bootstrap comes from `cdn.jsdelivr.net` without SRI in all four templates and breaks on an offline host. Embed it locally (the directory is already embedded).

### 4.5 Job lifecycle (corrected)

- **Panic recovery** on the job goroutine (`:290`): add it, but it only catches panics on that goroutine. Worker goroutines and the runtime's `fatal error: concurrent map` are not recoverable there; Phase 2 is the real fix.
- **No stop/cancel endpoint**, despite `WEB_JOB_QUEUE.md:11`. If `LaunchSession` hangs, `processingMu` is never released and every later job sits at "running" forever.
- **`StartAllQueued`** marks every job "running" (`:284-291`) before it waits on `processingMu` (`:329`). Add a `waiting` state; set `running`/`StartedAt` in `processJob` after the lock.
- **Session ↔ job matching by path** (`:190`, `:904`) is ambiguous. Corrected mechanism: `SessThreshold` defaults to 0, so finished sessions are removed normally. Stale entries come from early-error returns in `LaunchSession` (`Session.go:213`, `:292`) that skip `clearSession`, and re-uploads with the same name share a path. `QueuedJob.SessionID` exists for this and is never assigned: assign it and match on it.
- **Unbounded queue:** `jq.jobs` has no cap. (Dropped: the `completed` re-slice keeping the backing array alive; `append` reallocates, so retention is bounded.)
- **Mode combinations:** see D3. `-web` is ignored when `-nzbdir` is set (`main.go:86` `select{}` runs before `:114`), and **(new)** also when `-proxytcp`/`-proxytls` is set (else-if order).
- `webProcessor`/`webWg` are assigned twice (`main.go:127-128`, `webserver.go:481-482`). Not a race (the `go` statement orders them), just redundant. Remove the second.
- `webserver.go:349-360`: `wg.Add(1)`/`wg.Wait()` around a synchronous `LaunchSession` that already defers `Done()`. A no-op; remove.
- Status codes: not-found returns 400 at `:663`/`:678`, conflict returns 400 at `:678`/`:699`. Use 404/409.
- No graceful shutdown: add `srv.Shutdown(ctx)` on signal.

### 4.6 Templates (corrected)

- `jobs.html:231-235`: the "commented out" Config button is live. `html/template` strips the `<!--`/`-->` lines as JS comments and keeps the `if (canConfig)` block, so the button renders and the modal PATCHes at `:401-402`. Use `/* … */`, or remove it under D2.
- `base.html` is parsed but never used, and its keys don't match what the handlers pass. (Dropped: "would error at execute time"; tested, it renders with empty values.) Delete it, or make the pages extend it.
- `handleJobsList`/`handleStats` pass `jobs`/`stats` into templates that contain no template actions at all, so every page load pays for `ListJobs()` and throws the result away. Pass nothing.
- `index.html:169-176` `showNotification` inserts `${message}` without `escapeHtml`. Latent (callers at `:80`, `:85` pass static text). Escape it anyway.
- Fine as-is: templates and CSS are embedded (`webserver.go:17-18`, `:495`, `:503`); no `template.HTML` or `text/template`.

---

## Phase 5: NNTP proxy (corrected)

The proxy runs alone in its own mode (see Context), so its bugs affect only proxy clients, never NZB download workers. The cross-impact is in `NetConn.go`, marked below.

### 5.1 Hangs and resource exhaustion

- **No client deadlines.** The only `Set*Deadline` calls are on upstream connections (`ConnPool.go:383`, `:473`). `ProxySession.IsExpired()` (`ProxyHelper.go:322`) has no callers. Before login, unknown commands get `480` and the loop continues (`Server.go:286-289`), so a client can idle forever without logging in, and `MaxConns` only applies after login (`Server.go:214`). Account expiry *is* checked on every command (`:302`); only idle reaping is missing. **Fix:** read deadline per command, idle reaper, a cap on connections that haven't logged in.
- **Unlimited command line.** `textproto.Reader.ReadLine()` calls `readLineSlice(-1)` (go1.27.2 `net/textproto/reader.go:44`); the `len(line) > 128` check at `Server.go:93` runs after the whole line is in memory. **Fix (corrected):** a bounded reader that *replaces* `cliTp.R`, with a limit of **512 bytes** including CRLF (RFC 3977 §3.1). Not 128: message-IDs may be 250 octets (§3.6), and today `ARTICLE <long-id>` gets `501` and a disconnect.
- **Slow client holds an upstream connection.** `handleXOverCommand` (`Proxy.go:517`) and `handleXHdrCommand` (`:647`) call `ParkConn` only after writing to the client. Move the park before the write; it's safe because `ReadDotLines` has already buffered the response. (The draft swapped the two references: `handleGroupCommand` parks at `:273/:278`, `handleListCommand` at `:353`.)
- **`handleNextOrLastCommand` returns nothing** (`Proxy.go:745-782`) for any code except 223/421, including 422 and I/O errors with code 0. `LAST` is also answered with "421 No previous". Today GROUP leaves the pointer at `High`, so a normal newsreader doesn't hit 422. **It will once the GROUP → first-article fix below lands, so fix this first.** **Fix:** forward the upstream `code msg` for any code > 0; on code 0 try the next provider.
- **`XOVER 1-2-3` becomes `XOVER 0-`** (`Proxy.go:406-426`, sent at `:478-479`, read with no cap at `:499`). Return `501` unless there are exactly two parts. **(new)** `LIST` drops `args[1:]` (`Proxy.go:325-329`), so `LIST ACTIVE <wildmat>` fetches the full list. Cap `ReadDotLines` size.
- **Unbounded negative cache** (`ProxyHelper.go:428-439`; entries only cleared on lookup at `:406`). Numeric requests are keyed by bare article number (`Server.go:359-363`), so a miss in one group poisons the same number in every group for 60 s. Every `Set` also logs a line. **Fix:** size cap and janitor; key numeric entries by group + number.
- **Accept loop** (`Proxy.go:140-150`) compares an error string and spins at 100% CPU on `EMFILE`. Use `errors.Is(err, net.ErrClosed)` plus exponential backoff. Nothing ever closes the listener today, so the shutdown branch is unreachable; wire it to shutdown.

### 5.2 Correctness

- **Errors disconnect the client** (`Server.go:397-399` returns `502` plus an error; `:115-122` breaks the loop; same for syntax errors at `:332/:338/:403` and the pre-login `501` paths). RFC 3977 §3.2.1: unknown command → `500`, syntax error → `501`, and if the server does close, it must send `400` first.
  **Fix (corrected):** `handleRequest` uses its error return to mean "disconnect": `QUIT` (`:395`), login failures (`:304`) and client write failures (`:482`). Introduce a separate fatal/non-fatal result **before** making errors non-fatal, or `QUIT` stops working.
  **(dropped)** "There is no MODE case": `MODE READER` is handled before login (`Server.go:256-275`, answers `201`). After login, RFC 4643 §2.2 says the client must not send `MODE READER`.
  **(new)** `ARTICLE`/`HEAD`/`BODY`/`STAT` with no argument (allowed by the RFC; it means the current article) gets `501` and a disconnect.
- **Dead connection parked into the pool.** `handleGroupCommand` reaches `Proxy.go:278` (`ParkConn`) even when `ReadCodeLine` failed (`code == 0`). Park only `if code > 0`, as `:702-709`/`:722-728` already do. Proxy pool only.
- **Silent article truncation (corrected root cause; `NetConn.go`, shared).** With `-crc32 -yenctest=4` (4 is the default), a malformed article sets `brokenYenc = true; content = nil` (`NetConn.go:680`). The `isBadCrc || brokenYenc` → `99932` check at `:889` sits *inside* `if cfg.opt.YencCRC && !brokenYenc` (`:687`), so it never fires, `readDotLines` returns `1, nil`, and the client gets an empty `220`. **Dependencies:** download workers also handle `99932` (`Routines.go:267`), so they would start counting these as bad CRC; decide whether that's wanted. The `99932` path does `item.mux.Lock()` (`:890`), so 2.3 must land first.
- **Proxy rewrites relayed articles (corrected; `NetConn.go`, shared).** `Path: not-for-mail` is added at `NetConn.go:500`. Header stripping (`:520-536`) is `-cleanhdr` (not `-cleanheaders`), **on by default** (`Flags.go:42`). The default list (`Config.go:260-270`) strips `X-*`, `Path:`, `Xref:`, `Injection-`, `User-Agent:`, `Organization:`. It does **not** strip `References:` (the draft's threading claim is dropped), but it does break signatures that cover those headers. **Fix:** skip both rewrites when `proxy` is set, as `Date:` handling already does.
- **RFC deviations:**
  - GROUP sets the article pointer to `High` (`Proxy.go:289`); RFC 3977 §6.1.1 requires the first article. **Land after the NEXT/LAST fix.**
  - `ARTICLE <message-id>` resets the selected group (`Server.go:352`), which §6.2.1.2 forbids. **The reset is load-bearing:** `Server.go:413`/`:431` treat `ps.Group != "" && ps.MsgNum > 0` as "request by number". Add a local `byNumber` flag first, or message-ID requests get misrouted. `:357` also moves `MsgNum` before the request has succeeded.
  - Article number field: `0` is correct for message-ID requests (§6.2.1.2). Only numeric requests are wrong (the number is hardcoded and also lands in the message-ID slot, `ProxyHelper.go:40`, `Server.go:510`). Use the upstream response line for numeric requests.
- **`passwdMap` wiped in place.** `ProxyHelper.go:132-134` clears the live map before parsing. On `usersLoaded == 0` (`:204`) or `scanner.Err` (`:200`) nothing is restored. Reload runs only after a successful `verifyPassword` (`Server.go:167` before `:173-188`), so once the map is empty nobody can log in again until restart. **Fix:** parse into a new map and swap it in only on success. Policy note: emptying the file on purpose then no longer locks everyone out.
- **(dropped)** "The `.new.*` merge lets anyone add users": this merge is how `-proxyadduser` delivers new users (`Flags.go:176`), and anyone with write access to that directory can already replace `.proxypasswd`. **Keep it.**
- **`ProxySessions` keyed by username** (`Server.go:240`, deleted at `:44`): never read anywhere. Delete the map, or switch it to `map[uint64]` keyed by `ps.id`.
- **Plaintext password kept** in `ps.Password` (`Server.go:232`), never read. Delete the field.
- **Timing oracle for unknown users** (`ProxyHelper.go:219-221` returns before the sleep and bcrypt). Run bcrypt against a dummy hash of the same cost (`DefaultCost`) on the miss path.
- **`AUTHINFO PASS` split on whitespace** (`Server.go:100`, `:165`, plus `TrimSpace` at `:99`). (Corrected: RFC 4643 does not require taking the rest of the line; its grammar excludes spaces, and §2.3.2 makes whitespace a MAY.) The effect is the reverse of the draft: a password with spaces *fails*, and `correctpw <junk>` is *accepted*. Optional: reject extra arguments.
- **Client writes unchecked** almost everywhere (the `LinesWriter` error at `Server.go:481` is checked). Check them, so handlers stop fetching upstream for clients that are gone.
- `ps.Close()` (`ProxyHelper.go:311-319`) logs the username after clearing it, and would skip the deferred `CountConns` decrement (`Server.go:35`). Currently unreachable.
- `log.Fatalf` in the listener (`Proxy.go:117/126/133`) at startup only. It doesn't take an NZB session with it (the modes are separate), but a TLS failure kills the TCP listener too. Return an error instead.
- Two `bufio.Writer`s on one connection (`Server.go:235-236`). Use `ps.CliTp.W` for article data.
- **(new)** `ProxyParent` is published (`main.go:103`) and the proxy listener started (`:106`) before `LaunchSession` fills `providerList` (`Session.go:289`, `Utils.go:203`). Handlers read `providerList` without a lock: a data race, and an early client sees no providers. Start the listener after providers are loaded.

---

## Phase 6: Per-job settings (needs D2)

### 6.1 The per-job settings do nothing

The jobs page has a per-job settings dialog and a "default settings" dialog, with six fields: Check Only, Check First, CRC32 verification, yEnc test mode, yEnc CPU threads, cache directory. They're backed by `PATCH /api/jobs/:id/config` and `GET/PUT /api/jobs/default-config`. The code that would apply them is commented out (`webserver.go:332-348`, `/* disabled: data race! */`), so every job runs with the startup flags whatever you set. `WEB_JOB_QUEUE.md:105` and `:179` say otherwise. (The disabled code wouldn't have worked anyway: `cfg.opt` is a `*CFG`, so `oldConfig := cfg.opt` copies the pointer and the "restore" restores the same object.)

**Size of a real implementation (corrected):** the draft's "convert ~200 `cfg.opt.X` call sites to `s.opt.X`" is the wrong measure, and alone it would not work:
- 280 `cfg.opt.` uses in the five engine files (501 repo-wide), but most are debug flags. The six per-job fields appear on ~35 lines, which also include `Results.go` and `Yenc.go:42`.
- `Cachedir` and `CheckOnly` are built into the **global** `cache` at startup (`Flags.go:213-220`); `YencCpu` resizes the global `core_chan` (`Session.go:322`). Per-session copies of `cfg.opt` don't change those.
- Running jobs at the same time (removing `processingMu`) also needs per-session `memlim`, counters (`GCounter`, e.g. `postProviders` at `Workers.go:711`) and pools (3.11).

`JobConfig.Cachedir` (`webserver.go:613`) is a free-form filesystem path from the form. It must be restricted to an allowlist before it is ever applied.

### 6.2 Process-wide state changed by jobs (corrected)

- `Workers.go:40-42` sets `cfg.opt.ChanSize` from a job's segment count, but only when `-chansize <= 0` (default 1000).
- `memlim` is created by the long-lived web session at startup (`Session.go:302-312`), not by the first job. `Session.go:313-322` sets `YencCpu`/`YencAsyncCpu` and resizes `core_chan` process-wide.
- `Session.go:180-197` redirects the process-wide `log` output into the job's log file, but only with `-log`. (Dropped: "gin's access log included"; gin writes to stdout directly.) **(new)** With `-log`, the web session itself redirects to `logs/.log` (from `filepath.Base("")`) and never restores it.

### 6.3 `WEB_JOB_QUEUE.md` corrections

- Stop/cancel doesn't exist (`:11`).
- Config is never validated (`:83`); `YencTest` is clamped only on the `/upload` path.
- `Progress` is a 0..1 fraction, not 0–100 (`:54`).
- The route list (`:29-35`) omits `start-all` and both `default-config` endpoints.
- The `QueuedJob` struct (`:44-55`) is missing seven fields.
- `webserver.go` is new, not modified (`:193`); `base.html` and `style.css` are undocumented.
- Uploads are never cleaned up; `SessionID` is never assigned.
- `collectStats` filters on `session.active` (`webserver.go:854`), but the web session blocks at `Session.go:341` before `GoBootWorkers` sets `active = true`, so `/api/stats` shows no providers when no job is running.

---

## Phase 7: Smaller cleanups (corrected)

- `main.go:98-102` and `:122-126` build `&SESSION{...}` directly instead of via `newSession`, so `stopChan`, `counter`, `fileStat` and `WorkDividerChan` are nil. `<-s.stopChan` at `Session.go:341` blocks forever only because the channel is nil, and any future `close(s.stopChan)` on these sessions panics. Add a `newProxySession()` that sets the same fields.
- `ConnPool.go:498`: `if always || cfg.opt.BUG && ...` with `always` a `const true` (`Config.go:14`), so `c.mux.RLock()` is taken on every park. Performance only.
- **Queue counter ordering (corrected).** The draft's "Increase/Decrease mismatch → underflow" is wrong: every `Increase` (`Workers.go:572/674`) pairs with a successful push and each receive does one `Decrease` (`:305/:390`). The real bug: `Increase` runs *after* the non-blocking send. A fast worker (e.g. a cache hit) can `Decrease` first, which is clamped at 0 and logs an error. The late `Increase` then leaves `dlQueueCnt = 1` forever, so `GoWorkDivider` (`Workers.go:1029`) never ends. **Fix:** `Increase` before the send; undo it if the send fails.
- **(dropped)** `Cache.go:86-101` request/response correlation: concurrent requests are for the same item and get the same answer, and `pushDL` sends each item to only one group.

---

## Phase 8 (new): Pure-Go yEnc decoder using Go SIMD (prototype)

go1.27.x ships `simd` (portable, pure-Go fallback where hardware is missing) and `simd/archsimd` (amd64, arm64, wasm intrinsics, including carry-less multiply). Both require `GOEXPERIMENT=simd` and are outside the Go 1 compatibility promise. SIMD in Go wasn't available when the cgo build was chosen.

What the C library is still needed for: only mode-4 decoding. CRC already runs through Go's `hash/crc32` (already hardware-accelerated). Modes 1–3 already use the pure-Go `yenc` package.

Plan:
1. Write a pure-Go yEnc decoder behind a build tag (`//go:build goexperiment.simd`) with a scalar fallback, implementing the same interface as the rapidyenc wrapper (`AcquireDecoder`, `Read`, `Meta`).
2. Test it against `rapidyenc/yenc/*.yenc` and against rapidyenc output on generated articles (including escapes at line ends, `=y` lines inside the body, CRLF variations).
3. Benchmark against rapidyenc (MB/s, allocations) on amd64; on arm64 if hardware is available.
4. If close to rapidyenc: make it the default and drop cgo, the `.a`/`.dll` builds and the cross-compile toolchains. Otherwise keep the C build and revisit when `simd` leaves experimental status.

Independent of Phases 1–7; can run in parallel.

---

## Decisions

All three answered by the user on 2026-10-17.

**D1: Web login. DECIDED: separate web passwd file.**
- New flag (e.g. `-webpasswd`) pointing at its own file, same bcrypt format, loaded into its own map via the refactored `parsePasswdFile` (4.4). Proxy users get no web access.
- No web passwd file configured → the web server refuses to start unless it is bound to loopback. Even then, log a warning that it has no login.
- Rejected: reusing `.proxypasswd`, which would let every proxy user drive jobs on your provider accounts.

**D2: Per-job settings. DECIDED: show them disabled.**
- Keep the per-job and default-settings dialogs and the ⚙ button visible, with every field disabled and a "not yet supported, jobs use the startup settings" note.
- The three endpoints (`PATCH /api/jobs/:id/config`, `GET/PUT /api/jobs/default-config`) return `501 Not Implemented` instead of silently storing values that are never used. `/upload` ignores the config form fields (and says so in its response) instead of accepting them.
- Delete the commented-out block in `processJob` (`webserver.go:332-348`); it couldn't have worked (6.1).
- Fix `WEB_JOB_QUEUE.md:105`/`:179` to say per-job settings are planned, not active.
- Fix `jobs.html:231-235` so the button state is deliberate (disabled), not an accident of the `<!-- -->` comment (4.6).
- The real implementation (6.1 refactor) stays a separate later change, after Phases 2–3.

**D3: Startup mode combinations. DECIDED: reject with a clear error.**
- At startup, before any session is created, exit non-zero with a message for:
  - `-nzbdir` with `-web`;
  - `-nzbdir` with `-proxytcp`/`-proxytls`;
  - `-proxytcp`/`-proxytls` with `-web`;
  - `-nzbdir` alone (directory processing is unfinished: `processorThread` is a stub, see 2.1), until it is implemented.
- Exactly one of `-nzb`, `-web` or a proxy flag must be given.
- Running proxy and web together is not supported: it would need shared pools, or two sessions could exceed each provider account's connection limit.

---

## Execution order (corrected)

1. **Phase 1:** Go bump + dependency commit. Independent.
2. **Phase 2.1, 2.2, 2.3:** small, independent fixes.
3. **Phase 3.13 together with the replacement locking.** Revert the band-aids and add the local-build + locked-publish in one change.
4. **Settle the stop-channel design** (Phase 3 note), then **2.6 + 3.1 together** (single owner per group), then 2.4, 2.5, 3.6 (with the `MemLockWait` API change), 3.2, 3.4, 3.5, 3.7, then the rest of Phase 3, plus 3.10 (new boot hang).
5. **Phase 4** after D1/D3: 4.1, 4.2, 4.3 and the `ConnPool` nil panic from 3.14, then 4.4 (needs the passwd refactor from 5.2), then 4.5 and 4.6.
6. **Phase 5:** 5.1 first. In 5.2: the fatal/non-fatal split before "don't disconnect"; NEXT/LAST before GROUP → first article; the `byNumber` flag before removing the group reset; the `NetConn.go` changes gated on `proxy`, with 2.3 already in.
7. **Phase 6** per D2.
8. **Phase 7** cleanups.
9. **Phase 8** in parallel at any time.

## Verification (corrected)

**Never against live Usenet providers.** All runs that talk to a server use local go-while/nntp-server instances (`/tank0/efs/GO/src/github.com/go-while/nntp-server`), never `provider.json`. `race_linux.sh` hardcodes `-provider="provider.json"`, so don't run it as-is; use a copy that takes the provider file as an argument.

### Test setup (working, 2026-10-17)

- Binaries in `/tank0/tmp/nzbrex/bin`: `nntp-server` (built from the sibling checkout), `nntpseed`, and NZBreX before/after builds. Runs happen in `/tank0/tmp/nzbrex/run`, which has `provider.test.json` (A download-only, B upload target with `PreferIHAVE`) and a test proxy user in `.proxypasswd.new.*`.
- Checked so far: a download run from A → B with `-crc32 -yenctest=4` copies every article missing on B, and `STAT` on B confirms them.
- `Workers.go:695` declares `const testing = false` (unused), so tests in package `main` must import `testing` under an alias.
- Later: real data from the Debian NZBs, fetched once (read-only) via the cubenet4/cubenet5 account. See `nzbs/LOCAL_TESTING.md`. Uploads only ever go to the local servers.


- **Storage:** `/tank0/tmp/nzbrex/nntp-server/` (created), one directory per instance:
  - `a/` on `127.0.0.1:11119`: holds every test article.
  - `b/` on `127.0.0.1:11120`: missing a known subset, so NZBreX must download from A and re-upload to B.
  - each with `active/ ov/ storage/ history/ hashdb/ config.json users.json`.
- **nntp-server settings:** run with `-dropbinary=false` (the default rejects yEnc). `AcceptAllGroups: true`. A `Peers` entry for `127.0.0.1` without `ReadOnlyAccess`, so `IHAVE` is allowed. Reader accounts in `users.json`. Avoid port 1119, which is the NZBreX proxy default.
- **Ingest:** nntp-server accepts articles only via `IHAVE` (no `POST`). NZBreX re-uploads via `IHAVE` when the provider sets `PreferIHAVE`.
- **Test data:** the sample NZBs in `nzbs/` point at real Usenet articles and can't be used. Write a small seeding tool that generates yEnc multi-part articles (random payload, known CRC32), writes a matching `.nzb`, and feeds A (all) and B (subset) via `IHAVE`. Include deliberately broken articles (bad CRC, truncated yEnc) for 2.3/5.2.
- **`provider.test.json`:** two providers in different groups (needed for 2.5), A download-only and B with `PreferIHAVE: true`; a third provider with `MaxConns: 0` for 3.10.

### Checks

- `go build ./... && go vet ./...` after each phase; `go test -race ./rapidyenc/`.
- **Race detector run** (`go run -race -tags=other . -provider=provider.test.json -nzb=<generated>.nzb …`): 2.5, 3.9, 3.14 and 4.2 should report today and be silent afterwards.
- **2.1:** `-nzbdir` with a generated NZB: today the watcher hangs on the first tick. Afterwards the NZB appears in `sessMap`/`seenFiles` on every refresh. (It won't be processed; see D3.)
- **2.2:** `printf 'CAPABILITIES\r\n' | nc 127.0.0.1 <proxyport>` with the proxy pointed at server A: today the process dies; afterwards a capability list comes back.
- **2.3:** proxy with `-crc32`, then `ARTICLE` a deliberately broken article from server A: today it panics; afterwards an error response.
- **2.4 (corrected):** a **download** run with no upload provider (A only, `NoUpload` on everything else) over more segments than `2*totalMaxConns`. Today it stalls; afterwards it completes and `TOTAL_MemLock - TOTAL_MemReturned` (logged at `Workers.go:1001-1003`) returns to 0. A `-checkonly` run does *not* reproduce this.
- **3.1/3.2:** stop server B mid-run; `LaunchSession` must still return.
- **3.10:** a provider with `MaxConns: 0`: today boot hangs; afterwards it is skipped.
- **Goroutine counts** before/after N sequential sessions, via `SIGUSR1` (`setupSigusr1Dump`, `main.go:66`) or `SIGQUIT`, for 3.7.
- **Web:** `-web -webaddr 127.0.0.1:11911 -provider=provider.test.json`; run the `curl` examples from `WEB_JOB_QUEUE.md`; start two jobs; poll `/api/jobs` and `/api/stats` from two terminals in a loop under `-race`, including while a session shuts down (3.14 nil `ConnPool`). Start a second instance on the same port: it must exit non-zero (4.1). Check the D3 rejections.
- **Proxy:** point a newsreader (or a scripted `nc` session) at the proxy, backed by server A. Check `MODE READER`; `LAST` at the start of a group (after the GROUP fix); a 600-byte line (rejected without disconnect) and a 300-byte message-ID (accepted); `XOVER 1-2-3` (`501`); an unknown command (`500`, no disconnect); `QUIT` still closes; an abrupt disconnect mid-article. Provider connection counts must return to baseline.
- **Phase 8:** decoder tests against rapidyenc on the generated corpus; `go test -bench` comparison.
