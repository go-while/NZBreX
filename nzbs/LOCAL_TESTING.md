# Testing with these NZBs

These NZBs point at real Usenet articles. Tests must not run against live providers. All test runs, and **every upload**, go to local go-while/nntp-server instances only.

## Local test servers

| Server | Address | Storage | Content |
|---|---|---|---|
| A | `127.0.0.1:11119` | `/tank0/tmp/nzbrex/nntp-server/a/` | full article set |
| B | `127.0.0.1:11120` | `/tank0/tmp/nzbrex/nntp-server/b/` | partial set (re-upload target) |

Start each with `nntp-server -config <dir>/config.json -dropbinary=false` (the default rejects yEnc). A `127.0.0.1` peer entry with no `ReadOnlyAccess` allows `IHAVE` and reading without login.

Test runs use `/tank0/tmp/nzbrex/run/provider.test.json`, which points only at those two ports. Run from `/tank0/tmp/nzbrex/run`, which has no `provider.json`. `race_linux.sh` defaults to `-provider=provider.json`, so don't run it as-is.

Synthetic data: `go run ./tools/nntpseed -addr 127.0.0.1:11119 -files 2 -parts 10 -broken-crc 1 -broken-yenc 1 -nzb test.nzb -manifest test.manifest.json`. For server B add `-omit-every 3` (same `-seed`).

## TODO (later): real data from the Debian NZBs

Download the articles of the Debian NZBs once, from cubenet (read only: `ARTICLE`), and feed them into servers A and B with `IHAVE` (B with gaps). Then the real NZBs here work unchanged against the local servers.

| NZB | Segments | Size |
|---|---|---|
| `debian-11.6.0-amd64-netinst.iso.nzb.gz` | 581 | 410 MiB (start here) |
| `debian-12.9.0-amd64-netinst.nzb.gz` | 925 | 653 MiB |
| `debian-12.2.0-amd64-netinst.iso.nzb.gz` | 4020 | 2.8 GiB |
| `debian-12.9.0-amd64-DVD-1.iso.nzb.gz` | 5555 | 3.9 GiB |
| `debian-11.1.0-i386-DVD-1.nzb.gz` | 6560 | 4.1 GiB |

- Use the **cubenet4** or **cubenet5** account, for downloading and checking only. Credentials are probably in `../nntp-proxy/config*.json`. Never copy them into this repo.
- Never upload anything to cubenet or any other live provider.
- Note which articles cubenet is missing; they are useful "missing article" cases.
