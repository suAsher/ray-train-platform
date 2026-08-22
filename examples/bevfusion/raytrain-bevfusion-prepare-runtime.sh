#!/usr/bin/env bash
set -euo pipefail

# spk-rayjob/Ray place an immutable source archive in the runtime working
# directory.  The archive contains the branch Python source but intentionally
# not host-built CUDA extensions. Some legacy branches also relied on image
# modules that were never committed. Copy only files missing from the branch
# source so branch-owned code is never overwritten.
source_root="${RAYTRAIN_IMAGE_SOURCE_ROOT:-/opt/bevfusion/mmdet3d}"
runtime_root="${RAYTRAIN_SOURCE_ROOT:-$PWD}"
target_root="$runtime_root/mmdet3d"
prepare_lock="$runtime_root/.raytrain-prepare.lock.v2"
legacy_prepare_lock="$runtime_root/.raytrain-prepare.lock"
prepare_complete="$runtime_root/.raytrain-prepare.complete"
# YAPF 0.40.x generates versioned grammar pickles on first import. Eight local
# ranks importing it concurrently can observe a partially written cache file.
# Keep the cache with this node's extracted working directory and generate it
# while the existing preparation lock is held.
export XDG_CACHE_HOME="$runtime_root/.raytrain-cache"

if [[ -d "$source_root" && -d "$target_root" && ! -f "$prepare_complete" ]]; then
  owns_lock=false
  lock_candidate="$runtime_root/.raytrain-prepare.owner.$$"
  process_start="$(awk '{print $22}' "/proc/$$/stat" 2>/dev/null || true)"
  printf '%s %s %s\n' "$$" "$(date +%s)" "${process_start:-unknown}" > "$lock_candidate"

  file_identity() {
    stat -c '%d:%i' "$1" 2>/dev/null || stat -f '%d:%i' "$1" 2>/dev/null
  }

  file_age_seconds() {
    local modified now
    modified="$(stat -c '%Y' "$1" 2>/dev/null || stat -f '%m' "$1" 2>/dev/null)" || return 1
    now="$(date +%s)"
    printf '%s\n' "$((now - modified))"
  }

  cleanup_prepare_lock() {
    if [[ -e "$prepare_lock" ]] &&
      [[ "$(file_identity "$prepare_lock")" == "$(file_identity "$lock_candidate")" ]]; then
      rm -f "$prepare_lock"
    fi
    rm -f "$lock_candidate"
  }
  trap cleanup_prepare_lock EXIT

  recover_legacy_prepare_lock() {
    [[ -d "$legacy_prepare_lock" ]] || return 0
    local legacy_owner owner_pid
    legacy_owner="$legacy_prepare_lock/owner.pid"
    read -r owner_pid < "$legacy_owner" 2>/dev/null || owner_pid=""
    if [[ "$owner_pid" =~ ^[1-9][0-9]*$ ]] && ! kill -0 "$owner_pid" 2>/dev/null; then
      rm -f "$legacy_owner"
      rmdir "$legacy_prepare_lock" 2>/dev/null || true
    fi
  }
  recover_legacy_prepare_lock

  for _attempt in $(seq 1 3000); do
    # A v2 lock is always a regular file. A directory at this path can only be
    # residue from a broken/partial launcher. Remove it only when empty and
    # older than the acquisition grace period; never call ln against it.
    if [[ -d "$prepare_lock" ]]; then
      if [[ "$(file_age_seconds "$prepare_lock")" -ge 2 ]]; then
        rmdir "$prepare_lock" 2>/dev/null || true
        if [[ -d "$prepare_lock" ]]; then
          echo "invalid non-empty BEVFusion runtime lock directory: $prepare_lock" >&2
          exit 1
        fi
      fi
      if [[ -d "$prepare_lock" ]]; then
        sleep 0.1
        continue
      fi
    fi

    # The candidate already contains the owner before the hard link is made.
    # link(2) makes acquisition atomic: observers can never see an empty lock
    # created by a live owner between mkdir and writing owner.pid.
    if ln "$lock_candidate" "$prepare_lock" 2>/dev/null; then
      candidate_identity="$(file_identity "$lock_candidate" || true)"
      acquired_identity="$(file_identity "$prepare_lock" || true)"
      if [[ -f "$prepare_lock" ]] && [[ -n "$candidate_identity" ]] &&
        [[ "$acquired_identity" == "$candidate_identity" ]]; then
        owns_lock=true
        break
      fi
    fi
    [[ -f "$prepare_complete" ]] && break

    # A SIGKILL cannot run the owner's EXIT trap. A valid dead PID is stale
    # immediately. Empty/corrupt legacy locks are stale only after a grace
    # period. Re-check the inode immediately before removal so recovery cannot
    # delete a newly acquired lock.
    if [[ -f "$prepare_lock" ]]; then
      observed_identity="$(file_identity "$prepare_lock" || true)"
      if [[ -z "$observed_identity" ]]; then
        sleep 0.1
        continue
      fi
      read -r owner_pid owner_started owner_process_start < "$prepare_lock" || owner_pid=""
      stale_lock=false
      if [[ "$owner_pid" =~ ^[1-9][0-9]*$ ]]; then
        if ! kill -0 "$owner_pid" 2>/dev/null; then
          stale_lock=true
        elif [[ "$owner_process_start" =~ ^[0-9]+$ ]] && [[ -r "/proc/$owner_pid/stat" ]]; then
          current_process_start="$(awk '{print $22}' "/proc/$owner_pid/stat" 2>/dev/null || true)"
          [[ "$current_process_start" == "$owner_process_start" ]] || stale_lock=true
        elif [[ "$owner_started" =~ ^[0-9]+$ ]] &&
          (( $(date +%s) - owner_started >= 300 )); then
          # Portable fallback for systems without /proc. A healthy preparation
          # never owns the lock for five minutes; this also handles PID reuse.
          stale_lock=true
        fi
      elif [[ "$(file_age_seconds "$prepare_lock")" -ge 2 ]]; then
        stale_lock=true
      fi
      if [[ "$stale_lock" == true ]] && [[ -f "$prepare_lock" ]] &&
        [[ "$(file_identity "$prepare_lock")" == "$observed_identity" ]]; then
        rm -f "$prepare_lock"
      fi
    fi

    # Compatibility with a stale directory lock created by launcher-r6. It is
    # deliberately a different path from the v2 hard-link lock, so link(2)
    # can never succeed by creating a file inside this directory.
    recover_legacy_prepare_lock
    sleep 0.1
  done

  if [[ "$owns_lock" == true ]]; then
    if [[ ! -f "$prepare_complete" ]]; then
      while IFS= read -r -d '' source_file; do
        relative="${source_file#"$source_root"/}"
        target="$target_root/$relative"
        mkdir -p "$(dirname "$target")"
        if [[ ! -e "$target" ]]; then
          temporary_target="${target}.raytrain-tmp.$$"
          cp "$source_file" "$temporary_target"
          mv "$temporary_target" "$target"
        fi
      done < <(find "$source_root" -type f \( -name '*.so' -o -name '*.py' \) -print0)

      if python3 -c \
        'import importlib.util,sys;sys.exit(0 if importlib.util.find_spec("yapf") else 1)'; then
        python3 -c \
          'from yapf.yapflib.yapf_api import FormatCode; FormatCode("value = 1\n")'
      fi
      : > "$prepare_complete"
    fi

    cleanup_prepare_lock
    trap - EXIT
  elif [[ ! -f "$prepare_complete" ]]; then
    echo "timed out waiting for the BEVFusion runtime overlay" >&2
    exit 1
  else
    cleanup_prepare_lock
    trap - EXIT
  fi
fi

exec "$@"
