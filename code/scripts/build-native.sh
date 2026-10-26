#!/usr/bin/env bash
set -euo pipefail
root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
go_bin="${TOOLBOX_GO_BIN:-go}"
target="${1:-native}"
case "$target" in
  native)
    target_os="${GOOS:-$("$go_bin" env GOHOSTOS)}"
    target_arch="${GOARCH:-$("$go_bin" env GOHOSTARCH)}"
    output_name="toolbox"
    ;;
  linux-amd64|linux-arm64)
    target_os="linux"
    target_arch="${target#linux-}"
    output_name="toolbox-$target"
    ;;
  -h|--help)
    echo "Usage: $0 [native|linux-amd64|linux-arm64]"
    exit 0
    ;;
  *)
    echo "Unsupported target: $target (use native, linux-amd64 or linux-arm64)" >&2
    exit 1
    ;;
esac
if [[ $# -gt 1 ]]; then
  echo "Usage: $0 [native|linux-amd64|linux-arm64]" >&2
  exit 1
fi
echo "Building Toolbox for $target_os/$target_arch"
"$root_dir/scripts/prepare-web.sh"
mkdir -p "$root_dir/dist"
cd "$root_dir/backend"
CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" "$go_bin" build -trimpath -o "$root_dir/dist/$output_name" ./cmd/server
echo "Built $root_dir/dist/$output_name ($target_os/$target_arch)"
