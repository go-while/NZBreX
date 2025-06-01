#!/usr/bin/env bash

GaRuS="http://10.20.0.1:58080"
ROUTE="/upload.php"

DELAY=30
MAX_ATTEMPTS=30

set -e

GARUS_ROUTE="${GaRuS}${ROUTE}"
upload_with_retry() {
  local attempt=1
  local file="$1"
  local delay=$DELAY
  local max_attempts=$MAX_ATTEMPTS
  while [ $attempt -le $max_attempts ]; do
    test $attempt -gt 1 && echo "Upload attempt $attempt for $file..."
    size=$(du -b $file|cut -f1)
    human=$(du -h $file|cut -f1)
    if curl --silent -f -F "file=@$file" \
         -H "X-Git-Repo: $GITHUB_REPOSITORY" \
         -H "X-Git-Ref: $GITHUB_REF_NAME" \
         -H "X-Git-SHA7: $GITHUB_SHA7" \
         -H "X-Git-Comp: $COMPILER" \
         -H "X-Auth-Token: $BUILD_TEST_UPLOAD_TOKEN" \
         $GARUS_ROUTE; then
      echo "Upload succeeded for $file bytes=$size [$human]"
      return 0
    else
      echo "Upload failed for $file. Retrying in $delay seconds..."
      sleep $delay
      attempt=$(( attempt + 1 ))
    fi
  done
  echo "Upload failed for $file after $max_attempts attempts."
  return 1
}

ls -lha dist/
for file in dist/*.zip dist/*.exe dist/*.deb dist/*.tgz dist/*.tar.gz dist/*.xz dist/checksums.*; do
  [ -e "$file" ] || continue
  for algo in 256 512; do
    sha="sha${algo}sum"
    $sha "$file" > "$file.$sha"
    echo -e "\n$file.$sha"
    cat "$file.$sha" | cut -d" " -f1
    upload_with_retry "$file.$sha"
  done
  upload_with_retry "$file"
done

DIST="dist.$(date +%s).$(head -1 /dev/urandom |hexdump -n 4|head -1|cut -d" " -f2-|sed 's/\s//g').$(hostname).tgz"
if [ -e "$DIST" ]; then
  echo "$DIST already exists. Skipping tar."
else
  tar -czf "$DIST" dist/
  echo "Created $DIST from dist/."
  du -b "$DIST"; du -hs "$DIST";
  sha256sum "$DIST" > "${DIST}.sha256sum"
  sha512sum "$DIST" > "${DIST}.sha512sum"
  upload_with_retry "$file" "${DIST}.sha512sum"
  upload_with_retry "$file" "${DIST}.sha256sum"
  upload_with_retry "$file" "${DIST}"
fi

