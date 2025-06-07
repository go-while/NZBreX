[![Latest Release)](https://img.shields.io/github/v/release/go-while/NZBreX?logo=github)](https://github.com/go-while/NZBreX/releases/latest)

# NZB Refresh X (NZBreX)

**NZB Refresh X** (NZBreX) is a command-line tool to restore missing Usenet articles by downloading them from providers where they are still available and re-uploading them to others.

This tool is a modernized fork of [Tensai75/nzbrefresh](https://github.com/Tensai75/nzbrefresh).

---

## What It Does

NZBreX loads an NZB file (given as a command-line argument) and checks the availability of each article/segment across all configured Usenet providers (from `provider.json`).

If it finds articles missing from one or more providers but still available on at least one other, it will:

1. **Download** the missing article from a provider where it is present.
2. **Cache** the article locally (optional, depending on your config).
3. **Re-upload** the article to one or more providers where it was missing.

The tool uses the NNTP commands:
- **STAT** for checking
- **ARTICLE** for downloading
- **IHAVE** or **POST** for uploading

---

## Multiplexing / Grouping Provider Accounts

- You can group multiple accounts; articles will only be uploaded once per group.
- If `provider.Group` is not set, each provider with an empty group creates a group by `provider.Name`.
- **Check**, **Download**, and **Upload** can run concurrently or sequentially:
  - **Check** first (`-checkfirst`), then **Download** and **Upload** concurrently
  - Or with `-uploadlater` (TODO): **Download** only, then **Upload** later
  - Set `NoUpload` to skip uploading but allow download into cache.
- Upload priorities follow the order in `provider.json`.

---

## Behavior

- For **every connection**, 1 `go GoWorker(...)` is spawned with 3 private goroutines (Check, Down, Reup).
- The article body is uploaded exactly as originally received.
- Some non-essential headers may be removed during re-upload:

  ```go
  cleanHeader = []string{
    "X-",
    "Date:",
    "Nntp-",
    "Path:",
    "Xref:",
    "Cancel-",
    "Injection-",
    "User-Agent:",
    "Organization:",
  }
  ```
- The **Date** header is updated to the current date.

- Once successfully uploaded to one provider, the article should propagate to others.

---

## Cache

- The cache is a folder with subfolders for each NZB, holding downloaded **segment.art** files.
- The subfolder is the sha256 hash of the nzb file; **.art** files are hashed by `<messageID@xyz>` (including `<` and `>`).
- To fill your cache, set all providers to `'NoUpload': true`.
- The cache is checked before download requests; uploads are always fed from cache if available.
- DL/UL queues can rise and fall as items are checked and processed.
- Warning: the `-cc` (Check Cache on Boot) flag can be hard on traditional hard drives if `-crw` is set high (default 100).

---

## Installation

1. Compile from source or download the latest executable from the [Releases](../../releases) page.
2. Configure `provider.json` with your Usenet provider details.
3. Run the program from the command line:

   ```sh
   ./nzbrex -checkonly -nzb nzbs/ubuntu-24.04-live-server-amd64.iso.nzb.gz
   ./nzbrex -cd cache -checkfirst -nzb nzbs/ubuntu-24.04-live-server-amd64.iso.nzb.gz
   ./nzbrex --cd=cache --checkfirst --nzb=nzbs/ubuntu-24.04-live-server-amd64.iso.nzb.gz
   ```

---

## Running NZBreX

Run with the following arguments:

- You can use single `-` or double `--` for flags.
- `-help` prints the latest help with all arguments and info texts.

**Main flags:**

- `-nzb string` /path/file.nzb (default "test.nzb")
- `-provider string` /path/provider.json (default "provider.json")
- `-cc` [true|false] check NZB vs cache on boot (default: false)
- `-cd string` /path/to/cache/dir
- `-crw int` number of Cache Reader/Writer routines (default 100)
- `-crc32` [true|false] check CRC32 of articles while downloading (default: false)
- `-checkfirst` [true|false] if false: start downs/reups as soon as segments are checked (default: false)
- `-uploadlater` (TODO) [true|false] needs cache! start uploads when everything is cached (default: false)
- `-checkonly` [true|false] check online status only: no downs/reups (default: false)
- `-verify` [true|false] waits and tries to verify/recheck all reups (default: false)
- `-verbose` [true|false] more output (default: true)
- `-discard` [true|false] reduce console output to minimum (default: false)
- `-log` [true|false] logs to file (default: false)
- `-bar` [true|false] show progress bars (buggy) (default: false)
- `-bug` [true|false] full debug (default: false)
- `-debug` [true|false] part debug (default: false)
- `-debugcache` [true|false] (default: false)
- `-printstats int` print stats every N seconds. 0 is spammy, -1 disables output.
- `-print430` [true|false] print notice about error code 430 (article not found)
- `-slomoc int` sleep N ms before checking
- `-slomod int` sleep N ms before downloading
- `-slomou int` sleep N ms before uploading
- `-cleanhdr` [true|false] remove unwanted headers (default: true)
- `-cleanhdrfile` load unwanted headers from file
- `-maxartsize int` limit article size (default 1048576)
- `-mem int` limit memory usage to N segments in RAM (0 = auto)
- `-chansize int` set internal channel size for check/down/reup queues
- `-prof` start cpu+mem profiler
- `-profweb` start profiling webserver at address
- `-version` print app version
- `-yenccpu` limit parallel decoding with -crc32=true (default: 8)
- `-yencout` [true|false] write yenc parts to cache (needs -cd)
- `-yencmerge` [true|false] merge yenc parts into target files
- `-yencdelparts` [true|false] delete .part.N.yenc files after merge (only with -yencmerge)
- `-rapidyencbufsize` "set only if you know what you do! (default: 4K) (experimental/testing)"
- `-doublecheckrapidyenccrc` "[true|false] (experimental/testing)"
- `-yenctest` select mode 1 (bytes), 2 (lines), or 3 (direct) or 4 (rapidyenc) for -crc32. Mode 2 uses less memory, 3 is experimental. (default: 4)
- `-yencasync` limits async parallel decoding with -crc32=true and -yenctest=3 or 4. 0 defaults to runtime.NumCPU().
- `-debugsharedcc` debug sharedConn Chan (default: false)
- `-debugflags` debug item flags (default: false)
- `-debugcr` debug check routine (default: false)
- `-debugdr` debug downs routine (default: false)
- `-debugur` debug reups routine (default: false)
- `-debugstat` debug STAT (default: false)
- `-debugarticle` debug ARTICLE (default: false)
- `-debugihave` debug IHAVE (default: false)
- `-debugpost` debug POST (default: false)
- `-debugmemlim` debug MEMLIMIT (default: false)
- `-debugconnpool` debug ConnPool (default: false)
- `-debugrapidyenc` debug rapidyenc (default: false)
- `-logappend` append to logfile instead of rotating/overwriting (default: false)
- `-logdir` set log directory (default: logs)
- `-logold` rotate log files to .N, 0 disables rotation (default: 0)
- `-zzz-shr-testmode` only for test compilation on self-hosted runners (default: false)

---

## provider.json options per provider

- `Enabled`: true|false, enables/disables this provider
- `NoDownload`: true|false, disables downloads from provider
- `NoUpload`: true|false, disables upload to provider
- `Group`: string, group providers for deduplication
- `Name`: string, provider name (for debug/output)
- `Host`: string, server hostname or IP
- `Port`: int, server port
- `TCPMode`: string, tcp|tcp4|tcp6
- `PreferIHAVE`: bool, prefer IHAVE command
- `SSL`: bool, use SSL
- `SkipSslCheck`: bool, ignore certificate errors
- `Username`: string, account username
- `Password`: string, account password
- `MaxConns`: int, max connections
- `MaxConnErrors`: int, max consecutive fatal connection errors

---

## Terminal Output

In **-verbose** mode, NZBreX prints statistics while running:

- **STAT**: overall status of STAT command (checked on all providers)
- **DONE**: items downloaded/uploaded
- **SEGM**: segment availability (100% = all segments available somewhere)
- **DEAD**: segments not available on any provider
- **DMCA**: segments blocked by DMCA (code 451)
- **DL**: download queue status
- **HD**: cache status
- **UP**: reupload queue status
- **MEM N/N [C=0|D=0|U=0]**: memory slots and running routines

Percentages may be slightly off due to rounding. Rows may appear/disappear depending on matching numbers.

Example output:

```
2025/05/13 23:48:37  | DONE | [55.309%] (1219/2204) | SEGM [86.706%] (1911) | GETS [55.535%] (1224 / 1911 Q:687) | DISK [55.535%] (1224) | REUP [55.309%] (1219 / 1224 Q:5) | MEM:10/10 [C=5|D=5|U=4]
2025/05/13 23:48:40  |  DL  [ 55%]   SPEED:  5265 KiB/s | (Total: 865.00 MB)
2025/05/13 23:48:40  |  UL  [ 55%]   SPEED:  4838 KiB/s | (Total: 862.00 MB)
...
2025/05/13 23:52:43  |  STAT [100.0%] (2204) | DONE | [93.149%] (2053/2204) | SEGM [95.054%] (2095) | DEAD [4.946%] ( 109) | GETS [93.149%] (2053 / 2204 Q:151) | DISK [93.149%] (2053) | REUP [93.149%] (2053 / 2053 Q:0) | MEM:5/10 [C=0|D=5|U=0]
```

---

## TODOs
- Testing!

---

## Ideas and more TODOs
- #0001: watchDir (processor/sessions) almost done!
- #0010: fix all todos in code!
- #0020: better console output
- #0030: verify uploaded articles
- #0050: config.json does not work
- #0060: streaming (CHECK/TAKETHIS)
- #0080: progressbar (cosmetics)
- ...

---

## Contributing & Reporting Issues

Please open an issue on [GitHub Issues](../../issues) or a [Discussion](../../discussions) if you encounter problems.

> Only link NZB files if they are freeware (e.g. debian/ubuntu iso)!

---

- The **provider.ygg.json** works via yggdrasil network ;)
- Retention of the test servers is short. Articles can expire within minutes. Connections may drop or reply with unexpected codes at any time!
- If the yggdrasil test server does not work, do NOT open an issue! Wait, leave it alone, or run your own!

---

## Credits

This software is built using Go ([License](https://go.dev/LICENSE)).

Source based on ([github.com/Tensai75/nzbrefresh#commit:cc2a8b7](https://github.com/Tensai75/nzbrefresh/commit/cc2a8b7206b503bd27c23b5fb72797dc2dc34b39)) ([MIT License](https://github.com/Tensai75/nzbrefresh/blob/main/LICENSE.md))

This software uses the following external libraries:
- github.com/fatih/color#commit:4c0661 ([MIT License](https://github.com/fatih/color/blob/main/LICENSE.md))
- github.com/nu11ptr/cmpb ([MIT License](https://github.com/nu11ptr/cmpb/blob/master/LICENSE))
- github.com/Tensai75/nzbparser#commit:a1e0d80 ([MIT License](https://github.com/Tensai75/nzbparser/blob/master/LICENSE))
- github.com/Tensai75/cmpb#commit:16fb79f ([MIT License](https://github.com/Tensai75/cmpb/blob/master/LICENSE))
- github.com/go-while/go-cpu-mem-profiler ([MIT License](https://github.com/go-while/go-cpu-mem-profiler/blob/master/LICENSE))
- github.com/go-yenc/yenc ([MIT License](https://github.com/go-yenc/yenc/blob/master/LICENSE))

---

## Credits to Tensai75!
