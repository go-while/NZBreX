#!/usr/bin/env bash
#set -euo pipefail

# This script imitates GitHub Actions cache key and packs ~/.cache/go-build and ~/go/pkg/mod into a tarball.
# Instead of uploading, it moves the tarball to a local backup directory.

# Generate a cache key hash (mimicking actions/cache)
# We'll hash the contents of go.sum and go.mod as a typical Go cache would.
# You can adjust this to hash more files if you need.
GO_MOD="${1:-go.mod}"
GO_SUM="${2:-go.sum}"

if [[ ! -f $GO_MOD ]]; then
  echo "File $GO_MOD not found!"
  exit 1
fi
if [[ ! -f $GO_SUM ]]; then
  echo "File $GO_SUM not found!"
  exit 1
fi

CACHE_KEY="go-$(cat "$GO_MOD"; echo "::"; cat "$GO_SUM")"
HASH=$(echo -n "$CACHE_KEY" | sha256sum | awk '{print $1}')
TARFILE="cache-${HASH}.tgz"
DESTDIR="${HOME}/cache_backups"

mkdir -p "$DESTDIR"
if [[ -f "$DESTDIR/$TARFILE" ]]; then
 echo -n "$0: File exists "
 rm -fv "$DESTDIR/$TARFILE"
fi
TMP_TAR="${TARFILE}.tmp"
echo "Packing ~/.cache/go-build and ~/go/pkg/mod into $DESTDIR/$TMP_TAR ..."
START=$(date +%s)
tar czf "$DESTDIR/$TMP_TAR" -C "$HOME" .cache/go-build go/pkg/mod && mv -v "$DESTDIR/$TMP_TAR" "$DESTDIR/$TARFILE"
 if [[ $? -gt 0 ]]; then
  echo -n "$0: Error packing "
  rm -fv "$DESTDIR/$TARFILE"
  exit 1
 fi
echo "Cache tarball created: $(du -h $DESTDIR/$TARFILE)"
END=$(date +%s)
let took="END-START"
echo "$0: took $took seconds"

echo "Deleting any old cache files..."
find "$HOME/cache_backups" -type f -mtime +3 -name 'cache-*' -print -delete

END=$(date +%s)
let took="END-START"
echo "$0: took $took seconds"
exit 0
