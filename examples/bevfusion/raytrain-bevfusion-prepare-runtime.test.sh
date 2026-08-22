#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
temporary_dir="$(mktemp -d)"
trap 'rm -rf "$temporary_dir"' EXIT

grep -Fq 'prepare_lock=' "$repo_root/examples/bevfusion/raytrain-bevfusion-prepare-runtime.sh"
grep -Fq 'prepare_complete=' "$repo_root/examples/bevfusion/raytrain-bevfusion-prepare-runtime.sh"
grep -Fq 'XDG_CACHE_HOME' "$repo_root/examples/bevfusion/raytrain-bevfusion-prepare-runtime.sh"
grep -Fq 'ln "$lock_candidate" "$prepare_lock"' \
  "$repo_root/examples/bevfusion/raytrain-bevfusion-prepare-runtime.sh"
grep -Fq 'from yapf.yapflib.yapf_api import FormatCode' \
  "$repo_root/examples/bevfusion/raytrain-bevfusion-prepare-runtime.sh"

image_root="$temporary_dir/image/mmdet3d"
runtime_root="$temporary_dir/runtime"
mkdir -p "$image_root/runner" "$image_root/ops" "$runtime_root/mmdet3d"
printf '%s\n' 'class CustomEpochBasedRunner: pass' > "$image_root/runner/__init__.py"
printf '%s\n' 'binary-placeholder' > "$image_root/ops/voxelization_ext.so"
printf '%s\n' 'branch-owned' > "$runtime_root/mmdet3d/__init__.py"

RAYTRAIN_IMAGE_SOURCE_ROOT="$image_root" \
RAYTRAIN_SOURCE_ROOT="$runtime_root" \
"$repo_root/examples/bevfusion/raytrain-bevfusion-prepare-runtime.sh" \
  /bin/sh -lc '
    set -e
    test -f "$RAYTRAIN_SOURCE_ROOT/mmdet3d/runner/__init__.py"
    test -f "$RAYTRAIN_SOURCE_ROOT/mmdet3d/ops/voxelization_ext.so"
    test "$(cat "$RAYTRAIN_SOURCE_ROOT/mmdet3d/__init__.py")" = branch-owned
  '

# torchrun invokes the shell entrypoint once per local rank. Eight concurrent
# helpers on the same worker must prepare the shared working directory exactly
# once and no process may observe a partially copied Python/.so file.
concurrent_root="$temporary_dir/concurrent"
mkdir -p "$concurrent_root/mmdet3d"
fake_bin="$temporary_dir/bin"
prewarm_log="$temporary_dir/yapf-prewarm.log"
mkdir -p "$fake_bin"
cat > "$fake_bin/python3" <<'EOF'
#!/usr/bin/env sh
case "$*" in
  *find_spec*) exit 0 ;;
esac
printf 'prewarm\n' >> "$RAYTRAIN_PREWARM_LOG"
EOF
chmod 0755 "$fake_bin/python3"
pids=()
for rank in 0 1 2 3 4 5 6 7; do
  PATH="$fake_bin:$PATH" \
  XDG_CACHE_HOME="$temporary_dir/shared-cache" \
  RAYTRAIN_PREWARM_LOG="$prewarm_log" \
  RAYTRAIN_IMAGE_SOURCE_ROOT="$image_root" \
  RAYTRAIN_SOURCE_ROOT="$concurrent_root" \
  "$repo_root/examples/bevfusion/raytrain-bevfusion-prepare-runtime.sh" \
    /bin/sh -lc '
      test -s "$RAYTRAIN_SOURCE_ROOT/mmdet3d/runner/__init__.py"
      test -s "$RAYTRAIN_SOURCE_ROOT/mmdet3d/ops/voxelization_ext.so"
      test "$XDG_CACHE_HOME" = "$RAYTRAIN_SOURCE_ROOT/.raytrain-cache"
    ' &
  pids+=("$!")
done
child_status=0
for pid in "${pids[@]}"; do
  if ! wait "$pid"; then
    child_status=1
  fi
done
test "$child_status" -eq 0
test -f "$concurrent_root/.raytrain-prepare.complete"
test "$(wc -l < "$prewarm_log")" -eq 1

# A process killed while owning the directory lock must not brick all later
# ranks until a Pod restart. The next helper recovers a lock whose owner PID is
# no longer alive.
stale_root="$temporary_dir/stale"
mkdir -p "$stale_root/mmdet3d"
printf '%s\n' 99999999 > "$stale_root/.raytrain-prepare.lock.v2"
RAYTRAIN_IMAGE_SOURCE_ROOT="$image_root" \
RAYTRAIN_SOURCE_ROOT="$stale_root" \
"$repo_root/examples/bevfusion/raytrain-bevfusion-prepare-runtime.sh" /usr/bin/true
test -f "$stale_root/.raytrain-prepare.complete"
test ! -e "$stale_root/.raytrain-prepare.lock.v2"

# A killed owner can leave an empty/corrupt legacy lock. Once it is older than
# the grace period, a later rank must recover it instead of waiting five
# minutes for the Pod to be restarted.
empty_root="$temporary_dir/empty-owner"
mkdir -p "$empty_root/mmdet3d"
: > "$empty_root/.raytrain-prepare.lock.v2"
touch -t 200001010000 "$empty_root/.raytrain-prepare.lock.v2"
RAYTRAIN_IMAGE_SOURCE_ROOT="$image_root" \
RAYTRAIN_SOURCE_ROOT="$empty_root" \
"$repo_root/examples/bevfusion/raytrain-bevfusion-prepare-runtime.sh" /usr/bin/true
test -f "$empty_root/.raytrain-prepare.complete"
test ! -e "$empty_root/.raytrain-prepare.lock.v2"

# A stale directory lock from launcher-r6 must not turn hard-link acquisition
# into "create a file inside the directory". The v2 file lock remains atomic,
# and the obsolete directory is recovered separately.
legacy_root="$temporary_dir/legacy-directory"
mkdir -p "$legacy_root/mmdet3d" "$legacy_root/.raytrain-prepare.lock"
printf '%s\n' 99999999 > "$legacy_root/.raytrain-prepare.lock/owner.pid"
RAYTRAIN_IMAGE_SOURCE_ROOT="$image_root" \
RAYTRAIN_SOURCE_ROOT="$legacy_root" \
"$repo_root/examples/bevfusion/raytrain-bevfusion-prepare-runtime.sh" /usr/bin/true
test -f "$legacy_root/.raytrain-prepare.complete"
test ! -e "$legacy_root/.raytrain-prepare.lock"
test ! -e "$legacy_root/.raytrain-prepare.lock.v2"

# If a dead owner PID has been reused, the recorded process start value (or
# the portable age fallback) must prevent the unrelated live process from
# keeping the lock forever.
reused_pid_root="$temporary_dir/reused-pid"
mkdir -p "$reused_pid_root/mmdet3d"
printf '%s %s %s\n' 1 946684800 unknown > "$reused_pid_root/.raytrain-prepare.lock.v2"
RAYTRAIN_IMAGE_SOURCE_ROOT="$image_root" \
RAYTRAIN_SOURCE_ROOT="$reused_pid_root" \
"$repo_root/examples/bevfusion/raytrain-bevfusion-prepare-runtime.sh" /usr/bin/true
test -f "$reused_pid_root/.raytrain-prepare.complete"
test ! -e "$reused_pid_root/.raytrain-prepare.lock.v2"

# The v2 lock path is contractually a file. A stale empty directory at that
# path must be recovered before link(2), otherwise POSIX ln would create a
# child link and let every rank believe it owns the lock.
v2_directory_root="$temporary_dir/v2-directory"
mkdir -p "$v2_directory_root/mmdet3d" "$v2_directory_root/.raytrain-prepare.lock.v2"
touch -t 200001010000 "$v2_directory_root/.raytrain-prepare.lock.v2"
RAYTRAIN_IMAGE_SOURCE_ROOT="$image_root" \
RAYTRAIN_SOURCE_ROOT="$v2_directory_root" \
"$repo_root/examples/bevfusion/raytrain-bevfusion-prepare-runtime.sh" /usr/bin/true
test -f "$v2_directory_root/.raytrain-prepare.complete"
test ! -e "$v2_directory_root/.raytrain-prepare.lock.v2"

# A non-empty directory is not a valid v2 lock. Do not spin for five minutes
# or recursively delete unknown content; fail immediately with an actionable
# diagnostic after the grace period.
invalid_v2_root="$temporary_dir/invalid-v2-directory"
invalid_v2_log="$temporary_dir/invalid-v2-directory.log"
mkdir -p "$invalid_v2_root/mmdet3d" "$invalid_v2_root/.raytrain-prepare.lock.v2"
printf '%s\n' residue > "$invalid_v2_root/.raytrain-prepare.lock.v2/stale-owner-link"
touch -t 200001010000 "$invalid_v2_root/.raytrain-prepare.lock.v2"
if RAYTRAIN_IMAGE_SOURCE_ROOT="$image_root" \
  RAYTRAIN_SOURCE_ROOT="$invalid_v2_root" \
  "$repo_root/examples/bevfusion/raytrain-bevfusion-prepare-runtime.sh" \
    /usr/bin/true 2> "$invalid_v2_log"; then
  echo 'expected a non-empty v2 lock directory to fail' >&2
  exit 1
fi
grep -Fq 'invalid non-empty BEVFusion runtime lock directory' "$invalid_v2_log"

# Multiple ranks can observe and recover the same stale lock concurrently.
# Losing a stat/remove race must not terminate a waiter under `set -e`.
parallel_stale_root="$temporary_dir/parallel-stale"
parallel_stale_log="$temporary_dir/parallel-stale-prewarm.log"
mkdir -p "$parallel_stale_root/mmdet3d"
printf '%s\n' 99999999 > "$parallel_stale_root/.raytrain-prepare.lock.v2"
pids=()
for rank in 0 1 2 3 4 5 6 7; do
  PATH="$fake_bin:$PATH" \
  RAYTRAIN_PREWARM_LOG="$parallel_stale_log" \
  RAYTRAIN_IMAGE_SOURCE_ROOT="$image_root" \
  RAYTRAIN_SOURCE_ROOT="$parallel_stale_root" \
  "$repo_root/examples/bevfusion/raytrain-bevfusion-prepare-runtime.sh" /usr/bin/true &
  pids+=("$!")
done
child_status=0
for pid in "${pids[@]}"; do
  if ! wait "$pid"; then
    child_status=1
  fi
done
test "$child_status" -eq 0
test -f "$parallel_stale_root/.raytrain-prepare.complete"
test "$(wc -l < "$parallel_stale_log")" -eq 1
test ! -e "$parallel_stale_root/.raytrain-prepare.lock.v2"

echo 'raytrain BEVFusion compatibility overlay: ok'
