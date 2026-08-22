#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
temporary_dir="$(mktemp -d)"
trap 'rm -rf "$temporary_dir"' EXIT

source_dir="$temporary_dir/source"
mkdir -p "$source_dir/.git"
capture_path="$temporary_dir/preflight.py"
spk_rayjob_path="$temporary_dir/spk-rayjob"

printf '%s\n' \
  '#!/usr/bin/env bash' \
  'set -euo pipefail' \
  'while (($#)); do' \
  '  if [[ "$1" == "--entrypoint" ]]; then' \
  '    entrypoint="$2"' \
  '    printf "%s" "$entrypoint" > "$RAYCTL_CAPTURE"' \
  '    exit 0' \
  '  fi' \
  '  shift' \
  'done' \
  'exit 1' > "$spk_rayjob_path"
chmod 0755 "$spk_rayjob_path"

SPK_RAYJOB_BIN="$spk_rayjob_path" \
RAYCTL_CAPTURE="$capture_path" \
"$repo_root/examples/bevfusion/submit-preflight.sh" \
  "$source_dir" \
  'registry.example/bevfusion@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' \
  'bevfusion-preflight-test' \
  'sample-data'

python3 - "$capture_path" <<'PY'
import base64
import re
import sys

entrypoint = open(sys.argv[1], encoding="utf-8").read()
encoded = re.search(r"base64\.b64decode\('([^']+)'\)", entrypoint)
assert encoded, entrypoint
source = base64.b64decode(encoded.group(1)).decode("utf-8")
for expected in (
    "@ray.remote(num_gpus=1)",
    "ray.get(gpu_probe.remote",
    "torch.cuda.is_available()",
    "preflight.json",
):
    assert expected in source, expected
PY

echo 'submit-preflight GPU-worker contract: ok'
