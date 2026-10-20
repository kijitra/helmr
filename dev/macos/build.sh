#!/bin/sh
set -eu
cd "$(dirname "$0")"
[ "$(uname -s)" = Darwin ] && [ "$(uname -m)" = arm64 ] || { echo 'Apple Silicon macOS required' >&2; exit 1; }
[ "$(id -u)" != 0 ] || { echo 'Do not build/run as root' >&2; exit 1; }
umask 077
mkdir -p .build
(cd helper && CGO_ENABLED=1 go build -mod=readonly -trimpath -o ../.build/helmr-vz-probe .)
/usr/bin/codesign --force --sign - --entitlements entitlements.plist .build/helmr-vz-probe
/usr/bin/codesign --verify --strict .build/helmr-vz-probe
