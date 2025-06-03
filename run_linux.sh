#!/bin/bash
nzbfile=nzbs/debian-11.6.0-amd64-netinst.iso.nzb
test "$1" != "" && nzbfile="$1"

./NZBreX \
	-chansize=500 \
        -checkfirst=true -checkonly=false \
        -nzb="$nzbfile" -provider="provider.json" \
	-cd=/cache/nzbrex -cc=true \
	-debug=false -debugmemlim=false -debugcache=false \
	-debugsharedcc=false -debugconnpool=false -debugworker=false \
	-debugBUG=false -debugflags=false \
	-log=false -verbose=true -print430=false -crc32=false -yencout=false -yencmerge=false -yencdelparts=false \
        -cleanhdrfile=cleanHeaders.txt -prof=false

