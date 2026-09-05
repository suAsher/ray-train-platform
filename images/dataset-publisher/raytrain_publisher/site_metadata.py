"""Conservative site identity from publisher-owned source paths."""

from collections.abc import Mapping
import re
from typing import Any


def sample_site_id(sample: Mapping[str, Any]) -> str:
    """Return a site only for an unambiguous site/scene source layout.

    Older indexes may be relative to a scene or use another layout. An empty
    value means unknown, never an inferred site from a token or arbitrary root.
    Payload locators are deliberately not used as the source of this metadata.
    """
    path = sample.get("lidar_path")
    if path is None:
        payloads = sample.get("payloads", {})
        path = payloads.get("lidar") if isinstance(payloads, Mapping) else None
    if not isinstance(path, str) or not path or "\\" in path or ":" in path:
        return ""
    parts = path.split("/")
    if any(not part or part in {".", ".."} or part.strip() != part
           or any(ord(char) < 32 or ord(char) == 127 for char in part)
           for part in parts):
        return ""
    if parts[0] == "labeled":
        parts = parts[1:]
    if len(parts) < 3 or parts[1] != sample.get("scene"):
        return ""
    site = parts[0]
    if site in {"samples", "sweeps", "raw", "public", "labeled"} or not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_-]{0,127}", site):
        return ""
    return site
