#!/usr/bin/env bash

set -e

upload_with_retry() {
  local file="$1"
  local max_attempts=30
  local attempt=1
  local delay=30
  while [ $attempt -le $max_attempts ]; do
    test $attempt -gt 1 && echo "Upload attempt $attempt for $file..."
    if curl --silent -f -F "file=@$file" \
         -H "X-Git-Repo: $GITHUB_REPOSITORY" \
         -H "X-Git-Ref: $GITHUB_REF_NAME" \
         -H "X-Git-SHA7: $GITHUB_SHA7" \
         -H "X-Auth-Token: $BUILD_TEST_UPLOAD_TOKEN" \
         http://10.20.0.1:58080/upload.php; then
      echo "Upload succeeded for $file size=$(du -b $file|cut -f1)"
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

for file in dist/*.zip dist/*.deb; do
  [ -e "$file" ] || continue
  for algo in 256 512; do
    sha="sha${algo}sum"
    $sha "$file" > "$file.$sha"
    cat "$file.$sha"
    upload_with_retry "$file.$sha"
  done
  upload_with_retry "$file"
done
