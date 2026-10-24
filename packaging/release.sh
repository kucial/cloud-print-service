#!/usr/bin/env bash
# Builds the release files into dist/, all named <component>-<version>-<platform>:
#   cps-agent-<v>-{windows,linux}-{amd64,arm64}, cps-agent-<v>-asuswrt, cpsagent-<v>-koolshare,
#   and with --server cps-server-<v>-<os>-<arch> (cps + cpsctl); then SHA256SUMS over them.
# Used by .github/workflows/release.yml (GitHub) and release-agent.yml (Gitea).
#
#   packaging/release.sh <version> [--server]
#
# Needs Go, zip, tar and sha256sum. Run from anywhere; output goes to <repo>/dist.
set -euo pipefail

version=${1:?usage: packaging/release.sh <version> [--server]}
server=false
[[ "${2:-}" == --server ]] && server=true

root=$(cd "$(dirname "$0")/.." && pwd)
dist="$root/dist"
rm -rf "$dist"
mkdir -p "$dist"

archive() { # <dir name> <goos>: zip for Windows, tar.gz otherwise; removes the directory
	if [[ "$2" == windows ]]; then
		(cd "$dist" && zip -qr "$1.zip" "$1")
	else
		tar -C "$dist" -czf "$dist/$1.tar.gz" "$1"
	fi
	rm -rf "${dist:?}/$1"
}

for target in windows/amd64 windows/arm64 linux/amd64 linux/arm64; do
	goos="${target%/*}"; goarch="${target#*/}"
	ext=""; [[ "$goos" == windows ]] && ext=".exe"
	name="cps-agent-${version}-${goos}-${goarch}"
	mkdir -p "$dist/$name"
	GOOS=$goos GOARCH=$goarch CGO_ENABLED=0 go build -C "$root" -trimpath \
		-ldflags="-s -w -X main.version=${version}" \
		-o "$dist/$name/cps-agent${ext}" ./agent/cmd/cps-agent
	gui=()
	if [[ "$goos" == windows ]]; then
		# Desktop build: tray icon and a local page for the server address and token.
		GOOS=$goos GOARCH=$goarch CGO_ENABLED=0 go build -C "$root" -trimpath -tags gui \
			-ldflags="-H=windowsgui -s -w -X main.version=${version}" \
			-o "$dist/$name/cps-agent-gui.exe" ./agent/cmd/cps-agent
		gui=(
			"Desktop (no command line): double-click cps-agent-gui.exe, enter the server" \
			"address and a one-time token in the page that opens. A tray icon shows the" \
			"status; right-click it for Start at login. Use either this or the service below," \
			"not both." \
			"" \
			"Service:"
		)
	fi
	printf '%s\n' \
		"cps-agent ${version} (${goos}/${goarch})" \
		"" \
		${gui[@]+"${gui[@]}"} \
		"1. On the server:  cpsctl agent enroll --site <site> --id <pc-name>   -> one-time token" \
		"2. On this PC, from an elevated prompt:" \
		"     set CPS_AGENT_SERVER=http://<server>:8080" \
		"     cps-agent.exe -enroll <token> -site <site> -install" \
		"   Config and credential: %ProgramData%\\cps-agent\\agent.json" \
		"   Log:                   %ProgramData%\\cps-agent\\agent.log" \
		"3. Remove:         cps-agent.exe -uninstall" \
		> "$dist/$name/README.txt"
	archive "$name" "$goos"
done

# Koolshare software center package for ASUS routers. The software center takes the plugin
# name from the file name up to the first "-", so this one must start with "cpsagent-"
# (the installed plugin's name), not "cps-agent-".
"$root/packaging/koolshare/build.sh" "$version"
mv "$dist/cpsagent.tar.gz" "$dist/cpsagent-${version}-koolshare.tar.gz"
# The same agent for ASUS routers and AiMesh nodes without koolshare.
"$root/packaging/asuswrt/build.sh" "$version"
mv "$dist/cps-agent-asuswrt.tar.gz" "$dist/cps-agent-${version}-asuswrt.tar.gz"

if $server; then
	for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64; do
		goos="${target%/*}"; goarch="${target#*/}"
		ext=""; [[ "$goos" == windows ]] && ext=".exe"
		name="cps-server-${version}-${goos}-${goarch}"
		mkdir -p "$dist/$name"
		for b in cps cpsctl; do
			GOOS=$goos GOARCH=$goarch CGO_ENABLED=0 go build -C "$root" -trimpath -ldflags="-s -w" \
				-o "$dist/$name/$b${ext}" "./server/cmd/$b"
		done
		cp "$root/server/config.example.json" "$dist/$name/"
		printf '%s\n' \
			"Cloud Print Service ${version} (${goos}/${goarch}): the server (cps) and admin CLI (cpsctl)" \
			"" \
			"1. Copy config.example.json to config.json; replace the dev-key-... values with keys from" \
			"   'openssl rand -hex 24', list your printers, and remove \"store_driver\": \"memory\"" \
			"   so jobs and tokens are kept (SQLite, cps.db)." \
			"2. ./cps -config config.json, then open http://<server>:8080/ui/" \
			"3. PDF conversion for label, receipt and PCL printers needs poppler (pdftoppm) on the PATH." \
			"" \
			"Full guide: https://github.com/kucial/cloud-print-service/blob/main/docs/INSTALL.md" \
			> "$dist/$name/README.txt"
		archive "$name" "$goos"
	done
fi

(cd "$dist" && sha256sum -- * > SHA256SUMS)
ls -l "$dist"
