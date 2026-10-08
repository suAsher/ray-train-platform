#!/usr/bin/env bash
set -euo pipefail

if [[ "${OBS_N9E_ISOLATED_BUILDER:-}" != 1 ]]; then
  echo 'Run only in the approved builder isolated container.' >&2
  exit 2
fi
if [[ $# != 3 || -e "$3" ]]; then
  echo 'Usage: prepare-source.sh NIGHTINGALE_ARCHIVE LOCKED_ASSETS_DIRECTORY NEW_SOURCE_DIRECTORY' >&2
  exit 2
fi
archive="$1"
assets="$2"
destination="$3"
hardening_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
printf '%s  %s\n' 3f0f57fa061b26721d756599d3be23ce08a06baff80bd673d8f8f109fade11eb "$archive" | sha256sum -c -
test -s "$hardening_dir/assets.sha256"
(cd -- "$assets" && sha256sum -c "$hardening_dir/assets.sha256")
mkdir -p -- "$destination"
tar -xzf "$archive" --strip-components=1 -C "$destination"
grep -Fq 'github.com/redis/go-redis/v9 v9.0.2' "$destination/go.mod"
grep -Fxq 'github.com/redis/go-redis/v9 v9.0.2 h1:BA426Zqe/7r56kCcvxYLWe1mkaz71LKF77GwgFzSxfE=' "$destination/go.sum"
bash "$hardening_dir/prepare-redis.sh" patched "$destination/.observability-deps/go-redis"
(cd -- "$destination" && go mod edit -replace github.com/redis/go-redis/v9=./.observability-deps/go-redis)

# Copy only the reviewed original image's eight resources. Do not call fe.sh,
# rebuild sandbox inputs, fetch latest releases, or omit embedded-feature tags.
asset_paths=(
  front/statik/frontend.zip
  pkg/sandbox/embedassets/linux_amd64/bwrap
  pkg/sandbox/embedassets/linux_amd64/python-base.tar.gz
  pkg/sandbox/embedassets/linux_amd64/n9e-sandbox-init
  aiagent/skill/embedded/codeassets/n9e.tar.gz
  aiagent/skill/embedded/codeassets/categraf.tar.gz
  aiagent/skill/embedded/codeassets/fe.tar.gz
  aiagent/skill/embedded/codeassets/manifest.json
)
for relative in "${asset_paths[@]}"; do
  mkdir -p -- "$(dirname -- "$destination/$relative")"
  cp -- "$assets/$relative" "$destination/$relative"
done
# The archive includes an older generated statik.go; replace only its generated
# representation with the exact ZIP from the locked runtime image.
cat > "$destination/front/statik/statik.go" <<'GO'
package statik

import (
	_ "embed"

	"github.com/rakyll/statik/fs"
)

//go:embed frontend.zip
var originalFrontend string

func init() { fs.Register(originalFrontend) }
GO
