#!/usr/bin/env bash
set -euo pipefail

if [[ "${OBS_CNPG_ISOLATED_BUILDER:-}" != 1 ]]; then
  echo 'Run only in an isolated container on the approved builder.' >&2
  exit 2
fi
if [[ $# != 3 || ( "$1" != baseline && "$1" != patched ) ]]; then
  echo 'Usage: prepare-source.sh baseline|patched ARCHIVE NEW_SOURCE_DIRECTORY' >&2
  exit 2
fi
mode="$1"
archive="$2"
destination="$3"
if [[ -e "$destination" ]]; then
  echo 'Source destination must not exist; use a fresh isolated directory.' >&2
  exit 2
fi
hardening_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
printf '%s  %s\n' c4ebb2fc69572825fb67bff43fbff32cb00791f4e3bbd1ae02835cb25fbc2be0 "$archive" | sha256sum -c -
mkdir -p -- "$destination"
tar -xzf "$archive" --strip-components=1 -C "$destination"
if [[ "$mode" == patched ]]; then
  patch --batch --fuzz=0 -d "$destination" -p1 < "$hardening_dir/patches/0001-observability-auth.patch"
fi
