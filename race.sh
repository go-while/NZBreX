#!/bin/bash
nzbfile=/nzbs/debian-11.6.0-amd64-netinst.iso.nzb
test "$1" != "" && nzbfile="$1"
#./scripts/code/newBuiltNo.sh .builtno.counter
go run -race $(ls *.go|grep -v signals_windows) \
	-cc=true -cd /cache/nzbrex -nzb "$nzbfile" -verbose -checkfirst=false -checkonly=true -debugcache=false -prof=false -log=false -provider=provider.json -cleanhdrfile=cleanHeaders.txt -debug=false -crc32=false # -yencout=true -yencmerge=true -yencdelparts=true
