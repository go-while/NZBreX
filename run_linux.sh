#!/bin/bash
./local_build_linux.sh || exit 1
nzbfile=nzbs/debian-11.6.0-amd64-netinst.iso.nzb.gz
test "$1" != "" && nzbfile="$1"
rm -rf /cache/nzbrex/*
./NZBreX \
	-chansize=500 \
        -checkfirst=false -checkonly=false \
        -nzb="$nzbfile" -provider="provider.json" \
	-cd=/cache/nzbrex -cc=true \
	-debug=false -debugmemlim=false -debugcache=false \
	-debugsharedcc=false -debugconnpool=false -debugworker=false \
	-debugBUG=false -debugflags=false \
	-log=false -verbose=true -print430=false \
        -yenctest=4 -yencasync=0 -crc32=true -yencout=true -yencmerge=true -yencdelparts=true \
        -cleanhdrfile=cleanHeaders.txt -prof=false \
	-testrapidyenc=false

