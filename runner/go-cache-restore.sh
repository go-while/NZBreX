#!/usr/bin/env bash
set -euo pipefail

# This script restores Go build and module caches from a local tarball created by cache_sim.sh.
# Usage: ./cache_restore.sh [go.mod] [go.sum]
# It looks for the cache tarball in ~/cache_backups using the hash of go.mod and go.sum as the filename.

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

CACHE_KEY="go-${(
  cat "$GO_MOD"
  echo "::"
  cat "$GO_SUM"
)}"
HASH=$(echo -n "$CACHE_KEY" | sha256sum | awk '{print $1}')
TARFILE="cache-${HASH}.tgz"
CACHEDIR="${HOME}/cache_backups"

if [[ ! -f "$CACHEDIR/$TARFILE" ]]; then
  echo "Cache file $CACHEDIR/$TARFILE does not exist."
  exit 1
fi

echo "Restoring ~/.cache/go-build and ~/go/pkg/mod from $CACHEDIR/$TARFILE ..."
tar xzf "$CACHEDIR/$TARFILE" -C "$HOME"

echo "Cache restored."
