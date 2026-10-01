#!/usr/bin/env bash
# Run only in a disposable Docker container, using the image's default user.
# Feed this script on stdin; do not mount a workspace, home, socket or credentials.
# Example (run on the builder, with a candidate image already available):
# docker run --rm -i --network bridge --cpus 2 --memory 4g --pids-limit 256 \
#   --cap-drop ALL --cap-add SETUID --cap-add SETGID --cap-add CHOWN \
#   --cap-add DAC_OVERRIDE --cap-add FOWNER --cap-add FSETID --cap-add AUDIT_WRITE \
#   -e RAYTRAIN_SUDO_SMOKE=1 --entrypoint /bin/bash "$candidate_image" \
#   -s -- hello < images/environment-workspace/sudo_smoke.sh
# Do not add --user=root, --privileged, or --security-opt=no-new-privileges.
# APT uses the image's configured sources; this script never rewrites them.
set -euo pipefail

fail() {
  printf 'sudo smoke: FAIL: %s\n' "$1" >&2
  exit 1
}

[[ "${RAYTRAIN_SUDO_SMOKE:-}" == 1 && -f /.dockerenv ]] \
  || fail 'requires an explicitly opted-in disposable Docker container'
[[ $# -le 1 ]] || fail 'usage: sudo_smoke.sh [hello|tree|ed]'
smoke_package="${1:-hello}"
# These are small command-line packages without services. Keep arbitrary APT
# options, repository overrides and packages with daemons out of this check.
case "$smoke_package" in
  hello|tree|ed) ;;
  *) fail 'choose an uninstalled lightweight package: hello, tree or ed' ;;
esac

[[ "$(id -un)" == ray && "$(id -u)" != 0 ]] \
  || fail 'image default user must be the non-root ray user'
for smoke_tool in sudo apt-get dpkg-query timeout stat mktemp python; do
  command -v "$smoke_tool" >/dev/null 2>&1 || fail "required tool is missing: $smoke_tool"
done

smoke_dir="$(mktemp -d /tmp/raytrain-sudo-smoke.XXXXXXXX)"
smoke_log="$smoke_dir/check.log"
smoke_marker="$smoke_dir/root-marker"
cleanup() {
  local result=$?
  trap - EXIT
  # The ray-owned parent directory lets ray unlink the root-owned marker.
  # Only exact files created by this invocation are removed.
  if ! rm -f -- "$smoke_marker" "$smoke_log" || ! rmdir -- "$smoke_dir"; then
    printf 'sudo smoke: FAIL: temporary evidence cleanup failed\n' >&2
    [[ "$result" != 0 ]] || result=1
  fi
  exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# Capture command output in our private temporary directory. In particular,
# do not print APT URLs, configured credentials, or the ambient environment.
run_check() {
  local label="$1"
  shift
  local result=0
  "$@" >"$smoke_log" 2>&1 </dev/null || result=$?
  [[ "$result" == 0 ]] || fail "$label (exit $result)"
  printf 'sudo smoke: PASS: %s\n' "$label"
}

smoke_root_uid="$(sudo -n id -u 2>"$smoke_log")" \
  || fail 'sudo -n id must succeed without a password'
[[ "$smoke_root_uid" == 0 ]] || fail 'sudo did not obtain UID 0'
printf 'sudo smoke: PASS: default ray user can sudo to UID 0\n'

run_check 'root writes an isolated temporary marker' \
  sudo -n /bin/sh -c 'umask 077; printf "%s\n" root-created > "$1"' smoke "$smoke_marker"
[[ "$(stat -c '%u' "$smoke_marker")" == 0 && -s "$smoke_marker" ]] \
  || fail 'temporary marker must be nonempty and owned by root'
rm -f -- "$smoke_marker"
[[ ! -e "$smoke_marker" ]] || fail 'root-owned temporary marker was not removed'
printf 'sudo smoke: PASS: root-owned temporary marker removed\n'

command -v vim >/dev/null 2>&1 || fail 'vim must be preinstalled before the APT smoke operation'
run_check 'preinstalled vim is executable by ray' timeout 30s vim --version
[[ "$(command -v python)" == /opt/raytrain/environment/bin/python ]] \
  || fail 'default python must resolve to the managed environment'
run_check 'managed Python works before APT' timeout 120s python -c \
  'import sys, ray, torch; assert sys.prefix == "/opt/raytrain/environment"; assert ray.__version__ == "2.58.0"; assert torch.__version__.split("+")[0] == "2.4.1"; assert torch.version.cuda == "12.1"'

smoke_before="$(dpkg-query -W -f='${Status}' "$smoke_package" 2>/dev/null || true)"
[[ "$smoke_before" != 'install ok installed' ]] \
  || fail "$smoke_package is already installed; choose another supported package"

smoke_apt_options=(
  -o Acquire::Retries=0
  -o Acquire::http::Timeout=20
  -o Acquire::https::Timeout=20
  -o DPkg::Lock::Timeout=30
  -o Dpkg::Use-Pty=0
)
run_check 'sudo APT update succeeds using the image sources' \
  sudo -n timeout --signal=TERM --kill-after=10s 180s \
  apt-get "${smoke_apt_options[@]}" -o APT::Update::Error-Mode=any -qq update
run_check "sudo APT installs previously absent $smoke_package" \
  sudo -n timeout --signal=TERM --kill-after=10s 180s \
  env DEBIAN_FRONTEND=noninteractive \
  apt-get "${smoke_apt_options[@]}" -y -qq --no-install-recommends --no-upgrade --no-remove \
  install "$smoke_package"

smoke_after="$(dpkg-query -W -f='${Status}' "$smoke_package" 2>/dev/null)" \
  || fail 'the newly installed package is absent from dpkg'
[[ "$smoke_after" == 'install ok installed' ]] || fail 'the APT package is not fully installed'
run_check "new $smoke_package executable works as ray" timeout 30s "/usr/bin/$smoke_package" --version
run_check 'managed Python remains unchanged after APT' timeout 120s python -c \
  'import sys, ray, torch; assert sys.prefix == "/opt/raytrain/environment"; assert ray.__version__ == "2.58.0"; assert torch.__version__.split("+")[0] == "2.4.1"; assert torch.version.cuda == "12.1"'
run_check 'managed Python dependency consistency remains valid' timeout 120s python -m pip check
[[ "$(id -un)" == ray && "$(id -u)" != 0 ]] || fail 'the calling shell must remain ray'
printf 'sudo smoke: PASS: all checks; package change is confined to this disposable container\n'
# Do not apt-remove packages or modify the immutable image. docker --rm removes
# this container's writable layer; EXIT also removes our exact temporary files.
