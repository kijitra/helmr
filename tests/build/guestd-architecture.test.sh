#!/usr/bin/env bash
# Build-driver contract only: neither runtime artifacts nor execution support.
set -euo pipefail
repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mkdir "$tmp/bin"
cat > "$tmp/bin/go" <<'MOCK'
#!/bin/sh
set -eu
printf '%s\n' "${GOOS:-}" "${GOARCH:-}" "${GOAMD64:-}" "${GOARM64:-}" "${CGO_ENABLED:-}" "${GOTOOLCHAIN:-}" "${GOFLAGS:-}" "${GOEXPERIMENT:-}" "$@" > "$TEST_RECORD"
MOCK
chmod +x "$tmp/bin/go"
for arch in x86_64 aarch64; do
 expected=amd64
 if [ "$arch" = aarch64 ]; then expected=arm64; fi
 PATH="$tmp/bin:$PATH" TEST_RECORD="$tmp/actual" ARCH="$arch" \
 GUESTD_OUTPUT="$tmp/$arch/guestd" GOARM64=v9.5 GOAMD64=v4 GOFLAGS='invalid ambient flags' GOEXPERIMENT='invalid ambient experiment' \
 "$repo_root/scripts/build-guestd-linux.sh"
 printf '%s\n' linux "$expected" v1 v8.0 0 local '' '' build -trimpath -buildvcs=false '-ldflags=-s -w' -o "$tmp/$arch/guestd" ./cmd/guestd > "$tmp/expected"
 diff -u "$tmp/expected" "$tmp/actual"
done
for arch in arm64 riscv64 ''; do
 rm -f "$tmp/actual"
 if PATH="$tmp/bin:$PATH" TEST_RECORD="$tmp/actual" ARCH="$arch" GUESTD_OUTPUT="$tmp/rejected/guestd" "$repo_root/scripts/build-guestd-linux.sh" > "$tmp/rejected.log" 2>&1; then
  # Empty means the documented x86_64 default, not an unsupported architecture.
  [ -z "$arch" ] || { echo "unexpected supported architecture: $arch" >&2; exit 1; }
  [ "$(sed -n '2p' "$tmp/actual")" = amd64 ]
 else
  [ -n "$arch" ] && [ ! -f "$tmp/actual" ] && [ ! -d "$tmp/rejected" ]
 fi
done
printf 'ok - guestd architecture build driver\n'
