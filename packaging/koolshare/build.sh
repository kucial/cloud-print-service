#!/bin/sh
# Builds the koolshare software center package dist/cpsagent.tar.gz, for offline install
# on koolshare hnd/axhnd/axhnd.675x routers (TUF-AX3000, RT-AX86U, ...).
#
#   packaging/koolshare/build.sh [version]     default: git describe
#
# The software center takes the module name from the file name up to the first "-" or
# "_": cpsagent.tar.gz, or with a version, cpsagent-0.2.0.tar.gz.
set -eu

root=$(cd "$(dirname "$0")/../.." && pwd)
version=${1:-$(git -C "$root" describe --tags --always --dirty 2>/dev/null | sed 's/^agent-//')}
stage="$root/dist/koolshare"
pkg="$stage/cpsagent"

rm -rf "$stage"
mkdir -p "$pkg/bin"
cp -R "$root/packaging/koolshare/cpsagent/." "$pkg/"
printf '%s\n' "$version" > "$pkg/version"

# These routers run a 32-bit ARM userland, also on the 64-bit models.
GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0 go build -C "$root" -trimpath \
	-ldflags="-s -w -X main.version=${version}" \
	-o "$pkg/bin/cps-agent" ./agent/cmd/cps-agent

chmod 755 "$pkg/install.sh" "$pkg/uninstall.sh" "$pkg/scripts/"*.sh "$pkg/bin/cps-agent"
# No macOS metadata files (._*) in the archive.
COPYFILE_DISABLE=1 tar -C "$stage" -czf "$root/dist/cpsagent.tar.gz" cpsagent
rm -rf "$stage"

if command -v md5sum >/dev/null; then
	md5=$(md5sum "$root/dist/cpsagent.tar.gz" | cut -d' ' -f1)
else
	md5=$(md5 -q "$root/dist/cpsagent.tar.gz")
fi
echo "dist/cpsagent.tar.gz  version $version  md5 $md5"
