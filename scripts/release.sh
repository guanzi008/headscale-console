#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

VERSION="${1:-${VERSION:-}}"
XUANYU_SOURCE="${XUANYU_SOURCE:-$ROOT_DIR/../xuanyu}"
if [[ -z "$VERSION" ]]; then
	echo "usage: $0 <version>" >&2
	echo "example: $0 0.1.0" >&2
	exit 1
fi
if [[ ! "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]]; then
	echo "error: invalid version: $VERSION" >&2
	exit 1
fi
if [[ -n "$(git status --porcelain)" ]]; then
	echo "error: working tree is not clean; commit changes before releasing" >&2
	exit 1
fi
[[ -d "$XUANYU_SOURCE" ]] || { echo "error: xuanyu source not found: $XUANYU_SOURCE" >&2; exit 1; }

RELEASE_DIR="$ROOT_DIR/dist/release/v$VERSION"
PACKAGE_DIR="$RELEASE_DIR/package"
if [[ -e "$RELEASE_DIR" ]]; then
	echo "error: release directory already exists: $RELEASE_DIR" >&2
	exit 1
fi
mkdir -p "$PACKAGE_DIR/deploy/nginx" "$PACKAGE_DIR/docs"

echo "==> Building headscale-console $VERSION"
VERSION="$VERSION" ./build.sh --rebuild-headplane --output "$RELEASE_DIR/headscale-console-linux-amd64"
CONSOLE_VERSION="$("$RELEASE_DIR/headscale-console-linux-amd64" --version)"
[[ "$CONSOLE_VERSION" == "headscale-console $VERSION ("* ]] || {
	echo "error: unexpected headscale-console version: $CONSOLE_VERSION" >&2
	exit 1
}
echo "    $CONSOLE_VERSION"

echo "==> Building xuanyu"
(
	cd "$XUANYU_SOURCE"
	go test ./...
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w -X main.toolVersion=$VERSION" \
		-o "$RELEASE_DIR/xuanyu-linux-amd64" .
)
XUANYU_VERSION="$("$RELEASE_DIR/xuanyu-linux-amd64" version)"
[[ "$XUANYU_VERSION" == "xuanyu $VERSION" ]] || {
	echo "error: unexpected xuanyu version: $XUANYU_VERSION" >&2
	exit 1
}

echo "==> Packing release assets"
install -m 0755 "$RELEASE_DIR/headscale-console-linux-amd64" "$PACKAGE_DIR/headscale-console"
PACKAGE_VERSION="$("$PACKAGE_DIR/headscale-console" --version)"
[[ "$PACKAGE_VERSION" == "$CONSOLE_VERSION" ]] || {
	echo "error: packaged headscale-console differs from the built binary" >&2
	exit 1
}
install -m 0644 "$ROOT_DIR/config.example.yaml" "$PACKAGE_DIR/config.example.yaml"
install -m 0644 "$ROOT_DIR/deploy/nginx/headscale-console.conf" "$PACKAGE_DIR/deploy/nginx/headscale-console.conf"
install -m 0644 "$ROOT_DIR/docs/nginx.md" "$PACKAGE_DIR/docs/nginx.md"
install -m 0644 "$ROOT_DIR/README.md" "$PACKAGE_DIR/README.md"
tar -C "$PACKAGE_DIR" -czf "$RELEASE_DIR/headscale-console-linux-amd64.tar.gz" .

(
	cd "$RELEASE_DIR"
	sha256sum \
		headscale-console-linux-amd64 \
		headscale-console-linux-amd64.tar.gz \
		xuanyu-linux-amd64 >SHA256SUMS
)

echo
echo "Release assets: $RELEASE_DIR"
ls -lh "$RELEASE_DIR"
echo
cat "$RELEASE_DIR/SHA256SUMS"
