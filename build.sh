#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT_DIR"

HEADPLANE_SOURCE="${HEADPLANE_SOURCE:-$ROOT_DIR/../headplane}"
HEADSCALE_SOURCE="${HEADSCALE_SOURCE:-$ROOT_DIR/../headscale}"
NODE_IMAGE="${NODE_IMAGE:-node:24-slim}"
HOST_UID="$(id -u)"
HOST_GID="$(id -g)"
GOARCH="${GOARCH:-amd64}"
VERSION="${VERSION:-0.1.0}"
REBUILD_HEADPLANE=0
OUTPUT="${OUTPUT:-$ROOT_DIR/dist/headscale-console}"

usage() {
	cat <<'EOF'
Usage: ./build.sh [--rebuild-headplane] [--output PATH]

Environment:
  HEADPLANE_SOURCE    Headplane source checkout (default: ../headplane)
  HEADSCALE_SOURCE    Headscale source checkout (default: ../headscale)
  HEADPLANE_BUILD_DIR Existing Headplane build directory (default: <source>/build)
  NODE_IMAGE          Node 24 image used to extract the runtime (default: node:24-slim)
  GOARCH              linux target architecture (default: amd64)
  VERSION             headscale-console version (default: 0.1.0)
  OUTPUT              output path (default: dist/headscale-console)
EOF
}

while [ $# -gt 0 ]; do
	case "$1" in
		--rebuild-headplane) REBUILD_HEADPLANE=1 ;;
		--output)
			shift
			[ $# -gt 0 ] || { echo "error: --output requires a path" >&2; exit 1; }
			OUTPUT="$1"
			;;
		-h|--help) usage; exit 0 ;;
		*) echo "error: unknown argument: $1" >&2; usage >&2; exit 1 ;;
	esac
	shift
done

for command in go gzip tar sha256sum docker; do
	command -v "$command" >/dev/null 2>&1 || { echo "error: $command is required" >&2; exit 1; }
done
[ -d "$HEADPLANE_SOURCE" ] || { echo "error: Headplane source not found: $HEADPLANE_SOURCE" >&2; exit 1; }
[ -d "$HEADSCALE_SOURCE" ] || { echo "error: Headscale source not found: $HEADSCALE_SOURCE" >&2; exit 1; }

CACHE_DIR="$ROOT_DIR/.cache/build"
STAGE_DIR="$CACHE_DIR/headplane"
RUNTIME_DIR="$ROOT_DIR/internal/assets/runtime"
mkdir -p "$CACHE_DIR" "$RUNTIME_DIR" "$(dirname "$OUTPUT")"

if [ "$REBUILD_HEADPLANE" -eq 1 ]; then
	echo "==> Building Headplane in $NODE_IMAGE"
	HEADPLANE_BUILD_SOURCE="$CACHE_DIR/headplane-source"
	if [ -d "$HEADPLANE_BUILD_SOURCE" ] && ! rm -rf "$HEADPLANE_BUILD_SOURCE" 2>/dev/null; then
		echo "==> Fixing permissions on the previous Headplane build directory"
		docker run --rm -v "$HEADPLANE_BUILD_SOURCE:/cleanup" "$NODE_IMAGE" \
			chown -R "$HOST_UID:$HOST_GID" /cleanup
		rm -rf "$HEADPLANE_BUILD_SOURCE"
	fi
	mkdir -p "$HEADPLANE_BUILD_SOURCE"
	tar -C "$HEADPLANE_SOURCE" \
		--exclude=.git --exclude=node_modules --exclude=build --exclude=.cache \
		--exclude=docs --exclude=.react-router -cf - . |
		tar -C "$HEADPLANE_BUILD_SOURCE" -xf -
	docker run --rm --user "$HOST_UID:$HOST_GID" \
		-e HOME=/tmp/headplane-build-home \
		-v "$HEADPLANE_BUILD_SOURCE:/src" -w /src "$NODE_IMAGE" \
		sh -lc 'mkdir -p "$HOME" /tmp/headplane-build-bin && corepack enable --install-directory /tmp/headplane-build-bin && PATH="/tmp/headplane-build-bin:$PATH" ./build.sh --app --skip-pnpm-prune'
	HEADPLANE_BUILD_DIR="$HEADPLANE_BUILD_SOURCE/build"
else
	HEADPLANE_BUILD_DIR="${HEADPLANE_BUILD_DIR:-$HEADPLANE_SOURCE/build}"
fi

[ -f "$HEADPLANE_BUILD_DIR/server/index.js" ] || {
	echo "error: Headplane build missing at $HEADPLANE_BUILD_DIR" >&2
	echo "       build it first or rerun with --rebuild-headplane" >&2
	exit 1
}
[ -d "$HEADPLANE_SOURCE/drizzle" ] || { echo "error: Headplane drizzle migrations not found" >&2; exit 1; }

echo "==> Extracting Node runtime from $NODE_IMAGE"
NODE_CONTAINER="$(docker create "$NODE_IMAGE")"
cleanup_container() { docker rm -f "$NODE_CONTAINER" >/dev/null 2>&1 || true; }
trap cleanup_container EXIT
rm -f "$CACHE_DIR/node"
docker cp "$NODE_CONTAINER:/usr/local/bin/node" "$CACHE_DIR/node"
cleanup_container
trap - EXIT
chmod 0755 "$CACHE_DIR/node"
NODE_VERSION="$($CACHE_DIR/node --version)"
echo "    Node $NODE_VERSION"
NODE_NUMBER="${NODE_VERSION#v}"
NODE_MAJOR="${NODE_NUMBER%%.*}"
NODE_REST="${NODE_NUMBER#*.}"
NODE_MINOR="${NODE_REST%%.*}"
if [ "$NODE_MAJOR" -ne 24 ] || [ "$NODE_MINOR" -lt 2 ]; then
	echo "error: Headplane requires Node >=24.2 <25, got $NODE_VERSION" >&2
	exit 1
fi

echo "==> Building Headscale"
(
	cd "$HEADSCALE_SOURCE"
	CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH" go build -trimpath \
		-ldflags '-s -w' -o "$CACHE_DIR/headscale" ./cmd/headscale
)

echo "==> Packing Headplane runtime"
rm -rf "$STAGE_DIR"
mkdir -p "$STAGE_DIR/build" "$STAGE_DIR/drizzle"
cp -a "$HEADPLANE_BUILD_DIR/." "$STAGE_DIR/build/"
cp -a "$HEADPLANE_SOURCE/drizzle/." "$STAGE_DIR/drizzle/"
find "$STAGE_DIR" -type f -name '*.map' -delete
if [ -f "$HEADPLANE_SOURCE/public/hp_ssh.wasm" ]; then
	cp "$HEADPLANE_SOURCE/public/hp_ssh.wasm" "$STAGE_DIR/build/client/hp_ssh.wasm"
fi
if [ -f "$HEADPLANE_SOURCE/public/wasm_exec.js" ]; then
	cp "$HEADPLANE_SOURCE/public/wasm_exec.js" "$STAGE_DIR/build/client/wasm_exec.js"
fi

echo "==> Compressing embedded runtime"
gzip -9 -n -c "$CACHE_DIR/node" > "$RUNTIME_DIR/node.gz"
gzip -9 -n -c "$CACHE_DIR/headscale" > "$RUNTIME_DIR/headscale.gz"
tar -C "$STAGE_DIR" -cf - . | gzip -9 -n > "$RUNTIME_DIR/headplane.tar.gz"

BUILD_ID="$(date -u +%Y%m%d%H%M%S)-$(git rev-parse --short HEAD 2>/dev/null || echo dev)"
cat > "$RUNTIME_DIR/manifest.json" <<EOF
{"id":"$BUILD_ID","node":"$NODE_VERSION","goarch":"$GOARCH"}
EOF

COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
BUILT_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
LDFLAGS="-s -w"
LDFLAGS="$LDFLAGS -X github.com/guanzi008/headscale-console/internal/version.Version=$VERSION"
LDFLAGS="$LDFLAGS -X github.com/guanzi008/headscale-console/internal/version.Commit=$COMMIT"
LDFLAGS="$LDFLAGS -X github.com/guanzi008/headscale-console/internal/version.BuiltAt=$BUILT_AT"

echo "==> Building single binary"
CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH" go build -trimpath -tags netgo \
	-ldflags "$LDFLAGS" -o "$OUTPUT" ./cmd/headscale-console

echo
echo "Built: $OUTPUT"
du -h "$OUTPUT" | awk '{print "Size:  " $1}'
sha256sum "$OUTPUT" | awk '{print "SHA256: " $1}'
