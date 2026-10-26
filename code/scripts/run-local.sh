#!/usr/bin/env bash
set -euo pipefail
umask 077

root_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
binary="$root_dir/dist/toolbox"
data_dir="${TOOLBOX_DATA_DIR:-$root_dir/data}"

if [[ ! -x "$binary" ]]; then
  printf 'Missing executable: %s. Run scripts/build-native.sh first.\n' "$binary" >&2
  exit 1
fi

if [[ ! -d "$data_dir" ]]; then
  mkdir -m 700 -p "$data_dir"
fi
if [[ ! -O "$data_dir" || "$(stat -f '%Lp' "$data_dir")" != 700 ]]; then
  printf 'Data directory must be owned by the current user with mode 700.\n' >&2
  exit 1
fi
export SQLITE_PATH="${SQLITE_PATH:-$data_dir/toolbox.sqlite}"

# The binary creates and reuses local secrets on first run. This wrapper only
# selects a stable data directory so launch location does not change the DB.
exec "$binary" "$@"
