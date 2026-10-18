## Which file?

| You use | Download |
|---|---|
| Linux (any distro, also Alpine) | `NZBreX_@TAG@_linux-amd64_purego.tar.gz` (arm64: `linux-arm64`) |
| Debian / Ubuntu | `nzbrex_@VER@_amd64_purego.deb` (arm64: `_arm64_`), install with `sudo apt install ./nzbrex_….deb` |
| Windows | `NZBreX_@TAG@_windows-amd64_purego.zip` (ARM: `windows-arm64`) |
| macOS | `NZBreX_@TAG@_darwin-arm64_purego.tar.gz` (Intel: `darwin-amd64`) |
| FreeBSD | `NZBreX_@TAG@_freebsd-amd64_purego.tar.gz` |

**purego** is the default and fine for everyone. The **rapidyenc** files use rapidyenc's C decoder for `-yenctest=4`, which is faster on paper; decoding is far faster than any download either way. They exist for Linux amd64/arm64, Windows amd64 and macOS arm64; the Linux ones need glibc 2.35 or newer.

Each archive contains the binary, `README.md`, `LICENSE`, `cleanHeaders.txt` and the provider samples.

## Check the download

```sh
sha256sum -c SHA256SUMS --ignore-missing
```
