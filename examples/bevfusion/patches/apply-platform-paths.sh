#!/usr/bin/env bash
# Make a BEVFusion checkout read its dataset from wherever the platform mounts
# it, instead of the absolute paths baked into the *_infos_*.pkl index.
#
# Usage:
#   examples/bevfusion/patches/apply-platform-paths.sh /path/to/bevfusion-checkout
#
# The change is two things:
#   1. drop in mmdet3d/datasets/platform_paths.py (the resolver)
#   2. route the three path reads in NuScenesDataset.get_data_info through it
#
# Re-running is safe: an already-patched checkout is detected and skipped.
set -euo pipefail

readonly PATCH_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
checkout="${1:-}"
[[ -n "$checkout" ]] || { echo "usage: $0 <bevfusion-checkout>" >&2; exit 2; }
[[ -d "$checkout" ]] || { echo "not a directory: $checkout" >&2; exit 2; }

target="${checkout}/mmdet3d/datasets/nuscenes_dataset.py"
[[ -f "$target" ]] || { echo "not a BEVFusion checkout, missing: $target" >&2; exit 2; }

install -m 0644 "${PATCH_DIR}/platform_paths.py" "${checkout}/mmdet3d/datasets/platform_paths.py"
echo "installed mmdet3d/datasets/platform_paths.py"

python3 - "$target" <<'PY'
import io, sys

path = sys.argv[1]
with io.open(path, encoding="utf-8") as handle:
    source = handle.read()

# 1. Import the resolver with the other imports.  Do not insert it immediately
# before the class: both supported branches decorate that class, and putting
# an import between a decorator and its class produces invalid Python.  This
# also repairs checkouts touched by the previous installer version.
import_line = "from .platform_paths import DatasetPathResolver"
lines = [line for line in source.splitlines() if line.strip() != import_line]
try:
    import_index = lines.index("from .custom_3d import Custom3DDataset")
except ValueError:
    raise SystemExit("unexpected file layout: Custom3DDataset import not found")
lines.insert(import_index + 1, import_line)
source = "\n".join(lines) + ("\n" if source.endswith("\n") else "")

# Path reads and the resolver property already present means this is either a
# healthy second application or the invalid-import version repaired above.
already_patched = all(fragment in source for fragment in (
    "def _platform_paths(self) -> DatasetPathResolver:",
    'lidar_path=self._platform_paths.resolve(info["lidar_path"])',
    'self._platform_paths.resolve(camera_info["data_path"])',
))
if already_patched:
    compile(source, path, "exec")
    with io.open(path, "w", encoding="utf-8") as handle:
        handle.write(source)
    print("already patched; import placement verified", path)
    raise SystemExit(0)

# 2. Route the three recorded-path reads through the resolver. A per-instance
#    resolver is created lazily so datasets built before the platform sets the
#    environment variable still behave.
old_lidar = '            lidar_path=info["lidar_path"],\n            sweeps=info["sweeps"],'
new_lidar = ('            lidar_path=self._platform_paths.resolve(info["lidar_path"]),\n'
             '            sweeps=self._platform_paths.resolve_sweeps(info["sweeps"]),')
if old_lidar not in source:
    raise SystemExit("unexpected file layout: lidar_path/sweeps block not found")
source = source.replace(old_lidar, new_lidar, 1)

old_cam = '                data["image_paths"].append(camera_info["data_path"])'
new_cam = '                data["image_paths"].append(self._platform_paths.resolve(camera_info["data_path"]))'
if old_cam not in source:
    raise SystemExit("unexpected file layout: camera data_path append not found")
source = source.replace(old_cam, new_cam, 1)

# 3. Lazily construct the resolver. A property keeps the change confined and
#    avoids depending on this class's __init__ signature, which differs
#    between the bev_3dod and bev_3dod_s1h branches.
old_get = "    def get_data_info(self, index: int) -> Dict[str, Any]:"
new_get = '''    @property
    def _platform_paths(self) -> DatasetPathResolver:
        """Resolver that re-roots index paths onto the mounted dataset.

        Built on first use and cached: the dataset index records absolute
        paths from the machine that generated it, which are wrong under any
        other mount point.
        """
        resolver = getattr(self, "_platform_paths_cache", None)
        if resolver is None:
            resolver = DatasetPathResolver()
            self._platform_paths_cache = resolver
        return resolver

    def get_data_info(self, index: int) -> Dict[str, Any]:'''
if old_get not in source:
    raise SystemExit("unexpected file layout: get_data_info not found")
source = source.replace(old_get, new_get, 1)

with io.open(path, "w", encoding="utf-8") as handle:
    compile(source, path, "exec")
    handle.write(source)
print("patched", path)
PY

echo "done: ${checkout}"
