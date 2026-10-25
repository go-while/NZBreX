## Which file?

| You use | Download |
|---|---|
| Linux (any distro, also Alpine) | `NZBreX_@TAG@_linux-amd64_purego.tar.gz` (arm64: `linux-arm64`) |
| Debian / Ubuntu | `nzbrex_@VER@_amd64_purego.deb` (arm64: `_arm64_`), install with `sudo apt install ./nzbrex_….deb` |
| Windows | `NZBreX_@TAG@_windows-amd64_purego.zip` (ARM: `windows-arm64`) |
| macOS | `NZBreX_@TAG@_darwin-arm64_purego.tar.gz` (Intel: `darwin-amd64`) |
| FreeBSD | `NZBreX_@TAG@_freebsd-amd64_purego.tar.gz` |

**purego** is the default and fine for everyone. Its yEnc decoder (go-yenc-simd) is as fast as rapidyenc's C code on x86-64 CPUs with AVX2, and faster with AVX-512. On ARM (Apple Silicon, arm64 Linux) it uses NEON, about 5–8 GB/s on an Apple M1 or a Neoverse N2. The **rapidyenc** files decode with rapidyenc's C code for `-yenctest=4` instead. Decoding is far faster than any download either way; `NZBreX -benchmark` shows the numbers for your machine (the rapidyenc files measure both decoders). The rapidyenc files exist for Linux amd64/arm64, Windows amd64 and macOS arm64; the Linux ones need glibc 2.34 or newer and libstdc++, so on Alpine take purego.

Each archive contains the binary, `README.md`, `LICENSE`, `cleanHeaders.txt` and the provider samples.

## Check the download

```sh
sha256sum -c SHA256SUMS --ignore-missing
```
