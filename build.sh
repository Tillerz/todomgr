#!/usr/bin/env bash
# Builds portable binaries into dist/<platform>/ (one folder per platform, nothing to install).
# Usage: ./build.sh            (all platforms)   ./build.sh linux   (only this machine's Linux build)
set -euo pipefail
cd "$(dirname "$0")"
command -v go > /dev/null || { echo "go not found in PATH (is Go installed, and its bin folder in PATH?)"; exit 1; }
gofmt -l app | (! grep .) || { echo "gofmt: run 'gofmt -w app' first"; exit 1; }
go vet ./...
go test ./...

build() { # os arch outdir exe
  local dir="dist/$3"
  mkdir -p "$dir"
  GOOS=$1 GOARCH=$2 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "$dir/$4" ./app
  cp -f README.md LICENSE "$dir/"
  echo "built $dir/$4 ($(du -h "$dir/$4" | cut -f1))"
}

if [ "${1:-all}" = "linux" ]; then
  build linux amd64 todomgr-linux-amd64 todo
else
  build linux   amd64 todomgr-linux-amd64   todo
  build windows amd64 todomgr-windows-amd64 todo.exe
  build darwin  arm64 todomgr-macos-arm64   todo
  build darwin  amd64 todomgr-macos-amd64   todo

  # Release archives: dist/todomgr-v<Version>-<platform>.zip|.tar.gz + SHA256SUMS.txt (never with a data or ai folder).
  version=$(sed -n 's/^[[:space:]]*Version = "\(.*\)"$/\1/p' app/main.go)
  [ -n "$version" ] || { echo "no Version in app/main.go"; exit 1; }
  rm -f dist/*.zip dist/*.tar.gz dist/SHA256SUMS.txt
  (
    cd dist
    zip -qr "todomgr-v$version-windows-amd64.zip" todomgr-windows-amd64 -x 'todomgr-windows-amd64/data/*' 'todomgr-windows-amd64/ai/*'
    for p in linux-amd64 macos-arm64 macos-amd64; do
      tar -czf "todomgr-v$version-$p.tar.gz" --exclude="todomgr-$p/data" --exclude="todomgr-$p/ai" "todomgr-$p"
    done
    sha256sum *.zip *.tar.gz > SHA256SUMS.txt
  )
  echo "packed v$version:"; ls -1 dist/*.zip dist/*.tar.gz dist/SHA256SUMS.txt
fi
# The program code is in app/; the UI (app/web/) is embedded in the binary. Data is created next to the program in ./data on first start.
