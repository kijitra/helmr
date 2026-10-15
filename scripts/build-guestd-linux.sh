#!/bin/sh
set -eu

repo_root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
arch=${ARCH:-x86_64}

case "$arch" in
	x86_64) go_arch=amd64 ;;
	aarch64) go_arch=arm64 ;;
	*) echo "unsupported ARCH: $arch" >&2; exit 1 ;;
esac

output=${GUESTD_OUTPUT:-"$repo_root/dist/guestd/$arch/guestd"}
mkdir -p "$(dirname "$output")"

cd "$repo_root"
CGO_ENABLED=0 \
	GOOS=linux \
	GOARCH="$go_arch" \
	GOAMD64=v1 \
	GOARM64=v8.0 \
	GOFLAGS='' \
	GOEXPERIMENT='' \
	GOTOOLCHAIN=local \
	go build \
	-trimpath \
	-buildvcs=false \
	-ldflags="-s -w" \
	-o "$output" \
	./cmd/guestd
