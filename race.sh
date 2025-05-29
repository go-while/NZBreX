#!/bin/bash
nzbfile=/nzbs/debian-11.6.0-amd64-netinst.iso.nzb
test "$1" != "" && nzbfile="$1"
#./newBuiltNo.sh .builtno.counter
go run -race main.go Cache.go Config.go ConnPool.go Counter_uint64.go Flags.go MemLimit.go NetConn.go Results.go Routines.go Workers.go Utils.go Session.go Yenc.go \
	-cc=true -cd /cache/nzbrex -nzb "$nzbfile" -verbose -checkfirst=false -checkonly=true -debugcache=false -prof=false -log=false -provider=provider.json -cleanhdrfile=cleanHeaders.txt -debug=false -crc32=false # -yencout=true -yencmerge=true -yencdelparts=true 
