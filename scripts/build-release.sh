#!/usr/bin/env bash
# build-release.sh <purego|rapidyenc> <goos> <goarch> <version> [outdir]
#
# Builds one NZBreX binary and packs it for a release:
#   <outdir>/NZBreX_<version>_<goos>-<goarch>_<variant>.tar.gz   (.zip for windows)
#   <outdir>/nzbrex_<debversion>_<arch>_<variant>.deb            (linux, with DEB=1)
#
# purego:    CGO_ENABLED=0, decoder go-yenc-simd. Cross-compiles from anywhere;
#            static on linux. GOEXPERIMENT=simd unless SIMD=0.
# rapidyenc: cgo + -tags rapidyenc. The rapidyenc library for the target must be
#            built first (rapidyenc/build_rapidyenc_*.sh) and CC/CXX set for
#            cross builds.
#
# Used by .github/workflows/release.yml; runs the same locally.
set -euo pipefail

variant=${1:?variant: purego or rapidyenc}
goos=${2:?goos}
goarch=${3:?goarch}
version=${4:?version}
outdir=${5:-dist}

cd "$(dirname "$0")/.."
mkdir -p "$outdir"
outdir=$(cd "$outdir" && pwd)

name="NZBreX_${version}_${goos}-${goarch}_${variant}"
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT
mkdir -p "$stage/$name"

bin=NZBreX
[ "$goos" = windows ] && bin=NZBreX.exe

ldflags="-s -w -X main.appVersion=${version}"
tags=""
case "$variant" in
purego)
	export CGO_ENABLED=0
	if [ "${SIMD:-1}" = 1 ]; then export GOEXPERIMENT=simd; else export GOEXPERIMENT=; fi
	;;
rapidyenc)
	export CGO_ENABLED=1 GOEXPERIMENT=
	tags="-tags=rapidyenc"
	if [ "$goos" = windows ]; then
		# no MinGW runtime DLLs: only KERNEL32 and msvcrt remain
		ldflags="$ldflags -linkmode external -extldflags '-static -static-libgcc -static-libstdc++'"
	fi
	;;
*)
	echo "unknown variant '$variant' (purego or rapidyenc)" >&2
	exit 2
	;;
esac

echo "building $name (CGO_ENABLED=$CGO_ENABLED GOEXPERIMENT=${GOEXPERIMENT:-} $tags)"
GOOS=$goos GOARCH=$goarch go build -trimpath $tags -ldflags="$ldflags" -o "$stage/$name/$bin" .

cp README.md LICENSE cleanHeaders.txt provider.sample.json provider.ygg.json "$stage/$name/"
if [ "$variant" = rapidyenc ]; then
	cp rapidyenc/LICENSE "$stage/$name/LICENSE.rapidyenc-go"
	cp rapidyenc/rapidyenc/README.md "$stage/$name/README.rapidyenc.md" # its license terms (public domain / CC0)
	cp rapidyenc/rapidyenc/crcutil-1.0/LICENSE "$stage/$name/LICENSE.crcutil"
fi

(cd "$stage" && if [ "$goos" = windows ]; then
	zip -qr "$outdir/$name.zip" "$name"
else
	tar -czf "$outdir/$name.tar.gz" "$name"
fi)

if [ "${DEB:-0}" = 1 ] && [ "$goos" = linux ]; then
	# Debian versions start with a digit; '~' sorts pre-releases before the release
	debversion=${version#v}
	debversion=${debversion//-/\~}
	[[ $debversion =~ ^[0-9] ]] || debversion="0~$debversion"
	root="$stage/deb"
	mkdir -p "$root/DEBIAN" "$root/usr/bin" "$root/usr/share/doc/nzbrex"
	cp "$stage/$name/$bin" "$root/usr/bin/NZBreX"
	cp README.md LICENSE cleanHeaders.txt provider.sample.json provider.ygg.json "$root/usr/share/doc/nzbrex/"
	cat >"$root/DEBIAN/control" <<EOF
Package: nzbrex
Version: $debversion
Section: net
Priority: optional
Architecture: $goarch
Maintainer: ${DEB_MAINTAINER:-go-while <nzbrex@usenet-server.com>}
Homepage: https://github.com/go-while/NZBreX
Description: re-upload Usenet articles missing from providers
 NZBreX checks the articles of an NZB on your providers and re-uploads the
 missing ones. Build variant: $variant.
EOF
	dpkg-deb --root-owner-group --build "$root" "$outdir/nzbrex_${debversion}_${goarch}_${variant}.deb" >/dev/null
fi

ls -l "$outdir" | grep -F -e "$name" -e "_${goarch}_${variant}.deb" || true
