#!/usr/bin/env bash
set -euo pipefail
root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
go_bin="${TOOLBOX_GO_BIN:-go}"
host_os="$("$go_bin" env GOHOSTOS)"
host_arch="$("$go_bin" env GOHOSTARCH)"
target="${1:-$host_os-$host_arch}"
if [[ "$target" == "native" ]]; then target="$host_os-$host_arch"; fi
case "$target" in
  darwin-arm64|darwin-amd64|linux-amd64|linux-arm64|windows-amd64) ;;
  *) echo "Usage: $0 [native|darwin-arm64|darwin-amd64|linux-amd64|linux-arm64|windows-amd64]" >&2; exit 1 ;;
esac
target_os="${target%-*}"
target_arch="${target##*-}"
if [[ "$target_os" != "$host_os" && "$target_os" != "windows" ]]; then
  echo "Wails desktop packages require the target OS SDK. Build $target on a $target_os host; the pure Go server still supports cross-compilation." >&2
  exit 1
fi
export PATH="$(dirname "$go_bin"):$root_dir/.cache/bin:$PATH"
wails_bin="$root_dir/.cache/bin/wails"
if [[ ! -x "$wails_bin" ]]; then
  mkdir -p "$root_dir/.cache/bin"
  GOBIN="$root_dir/.cache/bin" "$go_bin" install github.com/wailsapp/wails/v2/cmd/wails@v2.17.0
fi
"$root_dir/scripts/prepare-web.sh"
tags="desktop"
if [[ "$target_os" == "linux" ]]; then tags="desktop,webkit2_41"; fi
cd "$root_dir/backend/cmd/server"
output_name=Toolbox
cgo_enabled=1
if [[ "$target_os" == "windows" ]]; then cgo_enabled=0; output_name=Toolbox.exe; fi
CGO_ENABLED="$cgo_enabled" "$wails_bin" build -s -m -skipbindings -skipembedcreate -nosyncgomod -compiler "$go_bin" -tags "$tags" -platform "$target_os/$target_arch" -o "$output_name"
if [[ "$target_os" == "darwin" ]]; then
  codesign --force --deep --sign - "$root_dir/desktop/build/bin/Toolbox.app"
  codesign --verify --deep --strict "$root_dir/desktop/build/bin/Toolbox.app"
fi
python3 "$root_dir/scripts/package-desktop.py" "$target_os" "$target_arch"
