#!/usr/bin/env bash
# Builds the Linux deployment package (run on the Mac).
#
#   deploy/build.sh [amd64|arm64]      default amd64 (uname -m = x86_64)
#
# Runs the tests first, then builds a static binary and packs it together
# with the interface files and the install script into dist/yt-stats-<version>-linux-<arch>.tar.gz.
set -euo pipefail

cd "$(dirname "$0")/.."

ARCH="${1:-amd64}"
case "$ARCH" in
  amd64|arm64) ;;
  *) echo "Nieznana architektura: $ARCH (dozwolone: amd64, arm64)" >&2; exit 2 ;;
esac

VERSION="$(git describe --always --dirty 2>/dev/null || echo dev)"
NAME="yt-stats-${VERSION}-linux-${ARCH}"
OUT="dist/${NAME}"

echo "==> Testy"
go vet ./...
go test -count=1 ./...
go test -count=1 -race ./...
if command -v node >/dev/null; then
  node --test test/js/*.test.js >/dev/null
  echo "ok   testy skryptu (node)"
fi

echo "==> Budowanie ${NAME}"
rm -rf "$OUT" "$OUT.tar.gz"
mkdir -p "$OUT"
start=$(date +%s)
CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" go build -trimpath \
  -ldflags="-s -w -X main.version=${VERSION}" \
  -o "$OUT/yt-stats" ./cmd/yt-stats
echo "Czas budowania: $(( $(date +%s) - start )) s (przyrostowo, z cache Go)"

cp -R web "$OUT/web"
cp -R deploy/nginx "$OUT/nginx"
cp deploy/yt-stats.service deploy/install.sh deploy/config.server.example.json deploy/README.md deploy/README_pl.md "$OUT/"
echo "$ARCH" > "$OUT/ARCH"
echo "$VERSION" > "$OUT/VERSION"

# No macOS metadata (._*, xattr), so GNU tar on the server prints no warnings.
COPYFILE_DISABLE=1 tar --no-mac-metadata --no-xattrs -C dist -czf "$OUT.tar.gz" "$NAME"
rm -rf "$OUT"

echo "==> Gotowe"
ls -lh "$OUT.tar.gz"
shasum -a 256 "$OUT.tar.gz"
cat <<EOF

Następnie:
  scp $OUT.tar.gz UŻYTKOWNIK@SERWER:/tmp/
  ssh UŻYTKOWNIK@SERWER
  cd /tmp && tar xzf ${NAME}.tar.gz && cd ${NAME} && sudo ./install.sh
EOF
