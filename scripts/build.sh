#!/bin/sh
set -eu

VERSION=${VERSION:-1.0.0}
OUT=${OUT:-dist}
mkdir -p "$OUT"

for ARCH in amd64 arm64; do
  STAGE=$(mktemp -d)
  trap 'rm -rf "$STAGE"' EXIT
  CGO_ENABLED=0 GOOS=linux GOARCH=$ARCH go build -trimpath \
    -ldflags "-s -w -X main.version=$VERSION" \
    -o "$STAGE/github-notes-archiver" ./cmd/github-notes-archiver
  cp deploy/git-ssh-wrapper "$STAGE/"
  cp deploy/github-notes-archiver.service "$STAGE/"
  cp deploy/uninstall.sh "$STAGE/"
  cp README.md "$STAGE/"
  chmod 0755 "$STAGE/github-notes-archiver" "$STAGE/git-ssh-wrapper" "$STAGE/uninstall.sh"
  chmod 0644 "$STAGE/github-notes-archiver.service" "$STAGE/README.md"
  tar --owner=0 --group=0 --mode='go-w' -czf "$OUT/github-notes-archiver_${VERSION}_linux_${ARCH}.tar.gz" -C "$STAGE" .
  rm -rf "$STAGE"
  trap - EXIT
done

(cd "$OUT" && sha256sum *.tar.gz > SHA256SUMS)
