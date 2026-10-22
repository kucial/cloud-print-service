#!/bin/sh
# Builds dist/cps-agent-asuswrt.tar.gz: cps-agent for ASUS routers and AiMesh nodes
# without the koolshare software center (ARMv7: TUF-AX3000, RT-AX86U, ...).
#
#   packaging/asuswrt/build.sh [version]     default: git describe
#
# On the device:  tar -xzf cps-agent-asuswrt.tar.gz -C /jffs
#                 /jffs/cps-agent/cps-agent.sh enroll <server-url> <token> [site] [id]
set -eu

root=$(cd "$(dirname "$0")/../.." && pwd)
version=${1:-$(git -C "$root" describe --tags --always --dirty 2>/dev/null | sed 's/^agent-//')}
stage="$root/dist/asuswrt"
pkg="$stage/cps-agent"

rm -rf "$stage"
mkdir -p "$pkg"
cp "$root/packaging/asuswrt/cps-agent.sh" "$pkg/"

# These routers run a 32-bit ARM userland, also on the 64-bit models.
GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0 go build -C "$root" -trimpath \
	-ldflags="-s -w -X main.version=${version}" \
	-o "$pkg/cps-agent" ./agent/cmd/cps-agent

chmod 755 "$pkg/cps-agent.sh" "$pkg/cps-agent"
COPYFILE_DISABLE=1 tar -C "$stage" -czf "$root/dist/cps-agent-asuswrt.tar.gz" cps-agent
rm -rf "$stage"
echo "dist/cps-agent-asuswrt.tar.gz  version $version"
