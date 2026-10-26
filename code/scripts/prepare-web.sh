#!/usr/bin/env bash
set -euo pipefail
root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root_dir/web"
npm ci --cache "$root_dir/.cache/npm"
npm run build
mkdir -p "$root_dir/backend/cmd/server/webdist"
find "$root_dir/backend/cmd/server/webdist" -mindepth 1 -maxdepth 1 -exec rm -rf {} +
cp -R "$root_dir/web/dist/." "$root_dir/backend/cmd/server/webdist/"
