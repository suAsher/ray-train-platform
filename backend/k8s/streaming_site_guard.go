package k8s

// This check lives in the server-generated command, not the training image:
// older images otherwise ignore the new environment and train the full version.
// Isolated Python excludes the uploaded workspace and PYTHONPATH from imports.
const streamingSiteRuntimeGuard = `import os, sys
try:
    from raytrain_runtime import managed_driver
    from raytrain_runtime.ray_data import StreamingDatasetConfig
except ImportError:
    raise SystemExit("Selected dataset sites require a site-aware Ray Train runtime image; upgrade the training image")
if getattr(managed_driver, "SITE_SELECTION_PROTOCOL", None) != 1 or "sites" not in getattr(StreamingDatasetConfig, "__dataclass_fields__", {}):
    raise SystemExit("Selected dataset sites require a site-aware Ray Train runtime image; upgrade the training image")
os.execvp(sys.argv[1], sys.argv[1:])
`
