#!/usr/bin/env bash
set -euo pipefail

# Some Docker 29 installations use a lazy content store for legacy base
# layers: `docker push` can report a missing blob even though `docker run`
# succeeds.  When that happens, flatten the just-built filesystem into one
# complete OCI layer, then let Docker push it using its protected login
# configuration.  Do not pass registry credentials as command-line arguments:
# those are visible to other local users through the process table.
#
# Usage:
#   examples/bevfusion/push-oci-image.sh registry.example/team/bevfusion:tag

target_image="${1:?target image is required}"
if ! command -v ctr >/dev/null 2>&1; then
  exec docker image push "$target_image"
fi

manifest_digest() {
  ctr -n moby images ls | awk -v ref="$target_image" '$1 == ref { print $3; exit }'
}

image_content_is_complete() {
  local manifest content_index digest
  manifest="$(manifest_digest)"
  [[ -n "$manifest" ]] || return 1
  content_index="$(ctr -n moby content get "$manifest" | python3 -c '
import json
import sys
manifest = json.load(sys.stdin)
print(manifest["config"]["digest"])
for layer in manifest.get("layers", []):
    print(layer["digest"])
')"
  while IFS= read -r digest; do
    [[ -n "$digest" ]] || continue
    ctr -n moby content ls | awk 'NR > 1 { print $1 }' | grep -Fqx "$digest" || return 1
  done <<<"$content_index"
}

flatten_target_image() {
  local cache_image container_id workdir user env_line
  local -a import_args

  cache_image="${target_image}-buildcache"
  container_id="$(docker create --entrypoint /bin/true "$target_image")"
  cleanup_container() { docker rm -f "$container_id" >/dev/null 2>&1 || true; }
  trap cleanup_container RETURN

  docker image tag "$target_image" "$cache_image"
  docker image rm "$target_image" >/dev/null

  workdir="$(docker image inspect "$cache_image" --format '{{.Config.WorkingDir}}')"
  user="$(docker image inspect "$cache_image" --format '{{.Config.User}}')"
  import_args=()
  if [[ -n "$workdir" ]]; then
    import_args+=(--change "WORKDIR $workdir")
  fi
  if [[ -n "$user" ]]; then
    import_args+=(--change "USER $user")
  fi
  while IFS= read -r env_line; do
    [[ -n "$env_line" ]] && import_args+=(--change "ENV $env_line")
  done < <(docker image inspect "$cache_image" --format '{{range .Config.Env}}{{printf "%s\\n" .}}{{end}}')

  # KubeRay supplies the container command (`ray start`), so a passive
  # entrypoint keeps the image usable in an interactive docker check as well.
  import_args+=(--change 'ENTRYPOINT ["tail","-f","/dev/null"]')
  # docker import prints its image ID to stdout.  Keep stdout reserved for the
  # cache tag returned to the caller, otherwise cleanup receives two values.
  docker export "$container_id" | docker image import "${import_args[@]}" - "$target_image" >&2
  trap - RETURN
  cleanup_container

  # The source build remains available until the OCI upload has succeeded.
  printf '%s\n' "$cache_image"
}

if ! image_content_is_complete; then
  echo "Docker content store is incomplete; creating a self-contained OCI image." >&2
  build_cache_image="$(flatten_target_image)"
else
  build_cache_image=""
fi

# After flattening, the target consists entirely of Docker-managed local
# content, so Docker can push it without exposing a credential in argv.
docker image push "$target_image"

if [[ -n "$build_cache_image" && "${KEEP_BUILD_CACHE:-false}" != "true" ]]; then
  docker image rm "$build_cache_image" >/dev/null || true
fi

docker image inspect "$target_image" --format '{{index .RepoDigests 0}}'
