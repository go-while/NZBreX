[![Latest Release)](https://img.shields.io/github/v/release/go-while/nzbrefreshX?logo=github)](https://github.com/go-while/nzbrefreshX/releases/latest)

# NZB Refresh X (NZBreX)

**NZB Refresh X**, or **NZBreX**, is a command-line utility designed to **restore missing Usenet articles** by downloading them from providers where they are still available and re-uploading them to others.

This tool is a revamped fork of [Tensai75/nzbrefresh](https://github.com/Tensai75/nzbrefresh).

---

## What It Does

NZBreX loads an NZB file (provided as a command-line argument) and checks the availability of each article/segment listed in the file across all configured Usenet providers (defined in `provider.json`).

If it finds articles that are missing from one or more providers—but still available on at least one other provider—it will:

1. **Download** the missing article from a provider where it is still present.
2. **Cache** the article locally (optional, based on your configuration).
3. **Re-upload** the article to one or more providers where it was missing.

- The tool uses the NNTP commands:

   **STAT** for checking

   **ARTICLE** for downloading

   **IHAVE** or **POST** for uploading

---

## Multiplexing / Grouping of Provider Accounts

> You can group multiple accounts together and articles will only be uploaded once to each group.

- If you don't set `provider.Group`: every **Provider Account** with an empty group creates a group by `provider.Name`.

- **Check**, **Download** and **Upload** can run concurrently as items fly in or sequential:

  **Check** first `-checkfirst` then: **Download** and **Upload** concurrently

  OR `-uploadlater` **Download only** and **Upload later** (uploadLater is still an idea and TODO)

  Set 'NoUpload' to not upload at all, but allow download into cache.

- Upload Priorities: the order is kept when reading the providers.json.

---

## Behavior

- For **every connection** spawns 1 `go GoWorker(...)` with 3 private **go func()** routines.

  These private routines per Worker execute requests: Check, Down, Reup

- The article body is uploaded exactly as it was originally.

- Some non-essential headers may be removed during re-upload:

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

- The **Date** header is updated to the current date.

- Once successfully uploaded to one provider, the article should propagate to others.

---

## Cache

- The cache is a folder with subfolders for each NZB, holding the downloaded **segment.art** files.

- The subfolder is the sha256 hash of the nzb file and **.art** files are hashed by **`<messageID@xyz>`** (including `<` and `>` !).

- To fill your cache with downloaded segments set all providers to: '  "NoUpload": true,  '

- The Cache is logically placed before executing download requests and uploads are fed always from cache if available.

- The DL/UL queues can rise and decrease while item are checked and moved around.

- Warning: cmd line arg `-cc` (Check Cache on Boot) can fuckup traditional harddisk drives if `-crw N` default value of 100 is used.

---

## Installation

- 1. Compile or run from source or get the latest executable for your operating system from the [Releases](../../releases) page if there is any ;)

- 2. Configure the `provider.json` file with the details of your Usenet providers.

- 3. open a cmd line and run the program:

  `./nzbrex -checkonly -nzb nzbs/ubuntu-24.04-live-server-amd64.iso.nzb.gz`

  `./nzbrex -cd cache -checkfirst -nzb nzbs/ubuntu-24.04-live-server-amd64.iso.nzb.gz`

  `./nzbrex --cd=cache --checkfirst --nzb=nzbs/ubuntu-24.04-live-server-amd64.iso.nzb.gz`


---

## Running NZBreX
Run in a cmd line with the following argument:

`Usage of nzbrex:`

- `go "flag" argument switches can be prefixed with single *-* or double *--* as you prefer!`

`-help` print the latest help with all arguments and info texts!

`-nzb string` /path/file.nzb (default "test.nzb")

`-provider string` /path/provider.json (default "provider.json")

`-cc` [true|false] checks nzb vs cache on boot (default: false)

`-cd string` /path/to/cache/dir

`-crw int` sets number of Cache Reader and Writer routines to equal amount (default 100)

`-crc32` [true|false] checks crc32 of articles on the fly while downloading (default: false)

`-checkfirst` [true|false] false: starts downs/reups asap as segments are checked (default: false)

`-uploadlater` (TODO!) [true|false] option needs cache! start uploads when everything got cached (default: false)

`-checkonly` [true|false] check online status only: no downs/reups (default: false)

`-verify` [true|false] waits and tries to verify/recheck all reups (default: false)

`-verbose` [true|false] a little more output than nothing (default: true)

`-discard` [true|false] reduce console output to minimum (default: false)

`-log` [true|false] logs to file (default: false)

`-bar` [true|false] show progress bars (buggy) (default: false)

`-bug` [true|false] full debug (default: false)

`-debug` [true|false] part debug (default: false)

`-debugcache` [true|false] (default: false)

`-printstats int` prints stats every N seconds. 0 is spammy and -1 disables output. a very high number will print only once it is finished

`-print430 int` [true|false] prints notice about err code 430 article not found

`-slomoc int`  SloMo'C' limiter sleeps N milliseconds before checking

`-slomod int`  SloMo'D' limiter sleeps N milliseconds before downloading

`-slomou int`  SloMo'U' limiter sleeps N milliseconds before uploading

`-cleanhdr` [true|false] removes unwanted headers. only change this if you know why! (default: true)

`-cleanhdrfile` loads unwanted headers to cleanup from cleanHeaders.txt

`-maxartsize int` limits article size (mostly articles have ~700K only) (default 1048576)

`-mem int` limit memory usage to N segments in RAM ( 0 defaults to number of total provider connections*2 or what you set but usually there is no need for more. if your 'MEM' is full: your upload is just slow. giving more mem will NOT help!)

`-chansize int` sets internal size of channels to queue items for check,down,reup. default should be fine.

`-prof` starts cpu+mem profiler: waits 20sec and runs 120sec

`-profweb` start profiling webserver at: '[::]:61234' or '127.0.0.1:61234' (default: empty = dont start websrv)

`-version` prints app version

`-yenccpu` limits parallel decoding with -crc32=true. 0 defaults to runtime.NumCPU() (default: 8)

`-yencout` [true|false] writes yenc parts to cache (needs -cd=/dir/) (experimental/testing) (default: false)

`-yencmerge` [true|false] merge yenc parts into target files (experimental/testing) (default: false)

`-yencdelparts` [true|false] delete .part.N.yenc files after merge (deletes parts only with -yencmerge=true) (experimental/testing) (default: false)

`-yenctest` select mode 1 (bytes) or 2 (lines) to use in -crc32. (experimental/testing) mode 2 should use less mem. (default: 2)


---

## provider.json options per provider

`"Enabled": true|false,` enables or disables this provider

`"NoDownload": true|false,` enables or disables downloads from provider

`"NoUpload": true|false,` enables or disables upload to provider

`"Group": "GroupA",` provider accounts can be grouped together so requests will go only once to same group

`"Name": "Provider 1",` arbitrary name of the provider, used in the debug text/output

`"Host": "",` usenet server host name or IP address

`"Port": 119,` usenet server port number

`"TCPMode": "tcp|tcp4|tcp6",` "tcp" uses ipv4 and ipv6, the others only one

`"PreferIHAVE": false,` prefers IHAVE command if provider has capabilities

`"SSL": false,` if true, secure SSL connections will be used

`"SkipSslCheck": false,` if true, certificate errors will be ignored

`"Username": "",` usenet account username

`"Password": "",` usenet account password

`"MaxConns": 50,` maximum number of connections to be used

`"MaxConnErrors": 3` maximum number of consecutive fatal connection errors after which the connection with the provider is deemed to have failed

---

## Terminal Output

NZBreX in **-verbose** mode prints some statistics while running:

- **STAT** : the overall status of STAT command. counts up if an item has been checked on all providers

- **DONE** : counts up if an item has been downloaded/uploaded, depending the cmdline arguments

- **SEGM** : shows availability of the parts:

   100% means: all segments are available, anywhere.

   Does not mean on all providers - but all segments should be there to get.

   Some provider report STAT with success but retrieving article fails with code 430 or 451.

- **DEAD** : segments not available on any provider are counted as dead

- **DMCA** : some providers return code 451 for banned articles

- **DL** : status of the download queue

- **HD** : cache status

- **UP** : status of the reupload queue

- **MEM N/N [C=0|D=0|U=0]** : shows usage of memory slots and running routines for Check, Download, Upload

- If your default MEM slots are always full, which is fine, your upload is just slow...

  ... or the tool got stuck on releasing memory and came to a full stop...

  ... giving more memory than default does not help with your upload speed.

  ... default is `total_maxconns*2` which keeps 1 item in queue for every provider connection, while it is processing an item ;)

- Note: all percentages can lie a little because of rounding errors!

- Sometimes rows appear or disappear: this depends on (not) matching numbers.

```
2025/05/13 23:48:37  | DONE | [55.309%] (1219/2204) | SEGM [86.706%] (1911) | GETS [55.535%] (1224 / 1911 Q:687) | DISK [55.535%] (1224) | REUP [55.309%] (1219 / 1224 Q:5) | MEM:10/10 [C=5|D=5|U=4]
2025/05/13 23:48:40  |  DL  [ 55%]   SPEED:  5265 KiB/s | (Total: 865.00 MB)
2025/05/13 23:48:40  |  UL  [ 55%]   SPEED:  4838 KiB/s | (Total: 862.00 MB)
...
2025/05/13 23:52:43  |  STAT [100.0%] (2204) | DONE | [93.149%] (2053/2204) | SEGM [95.054%] (2095) | DEAD [4.946%] ( 109) | GETS [93.149%] (2053 / 2204 Q:151) | DISK [93.149%] (2053) | REUP [93.149%] (2053 / 2053 Q:0) | MEM:5/10 [C=0|D=5|U=0]
```

---

## TODOs for ...
- you: testing!

---

## Ideas and more TODOs
- #0010: any bugs? fix all todos in code!
- #0020: better console output
- #0030: verify of uploaded articles
- #0050: config.json does not work
- #0060: streaming (CHECK/TAKETHIS)
- #0070: watchDir (processor/sessions)
- #0080: progressbar (cosmetics)
- #0090: disabled csv output as i don't need it
- ...?

---

## Contributing & Reporting Issues

Please open an issue on the [GitHub Issues](../../issues) page if you encounter problems or open a [Discussion](../../discussions)

> Only add a link to the NZB file if it is freeware like debian/ubuntu iso!

---

- The **provider.ygg.json** works via yggdrasil network ;)

---

- Retention of the Test-Servers is short. Articles can expire within few minutes.

  Connections can drop or reject or reply with unexpected return codes at any time!

  If yggdrasil Test-Server does not work... do NOT open an issue!

  Wait, leave it alone, do not disturb and run your own!

---

## Credits
This software is built using golang ([License](https://go.dev/LICENSE)).

Source based on ([github.com/Tensai75/nzbrefresh#commit:cc2a8b7](https://github.com/Tensai75/nzbrefresh/commit/cc2a8b7206b503bd27c23b5fb72797dc2dc34b39)) ([MIT License](https://github.com/Tensai75/nzbrefresh/blob/main/LICENSE.md))

This software uses the following external libraries:
- github.com/fatih/color#commit:4c0661 ([MIT License](https://github.com/fatih/color/blob/main/LICENSE.md))
- github.com/nu11ptr/cmpb ([MIT License](https://github.com/nu11ptr/cmpb/blob/master/LICENSE))
- github.com/Tensai75/nzbparser#commit:a1e0d80 ([MIT License](https://github.com/Tensai75/nzbparser/blob/master/LICENSE))
- github.com/Tensai75/cmpb#commit:16fb79f ([MIT License](https://github.com/Tensai75/cmpb/blob/master/LICENSE))
- github.com/go-while/go-cpu-mem-profiler ([MIT License](https://github.com/go-while/go-cpu-mem-profiler/blob/master/LICENSE))
- github.com/go-yenc/yenc ([MIT License](https://github.com/go-yenc/yenc/blob/master/LICENSE))


---

## Credits to Tensai75 !
