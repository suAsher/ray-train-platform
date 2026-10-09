"""Convert file-backed Envoy leaf identities to filesystem SDS, without I/O.

Only leaf certificate/key pairs rotate automatically. Trust roots remain in the
original validation_context and require a separate reviewed CA rollover.
Mount each Kubernetes TLS Secret as a directory (never individual subPath files)
and use direct paths like /etc/obs-mtls/tls.crt and /etc/obs-mtls/tls.key. Envoy
watches their parent, where kubelet atomically replaces the ..data symlink.

The returned files contain public configuration and filenames, never key data.
Place them next to the bootstrap in its existing ConfigMap directory and pass
that directory as sds_directory. No SDS server or extra process is necessary.
Existing dynamic SDS contexts are preserved; on a second conversion retain the
previously generated files because the return mapping only contains new files.
"""

from copy import deepcopy
import hashlib
import json
from pathlib import PurePosixPath
import re


SECRET_TYPE = "type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.Secret"


class SDSError(ValueError):
    """Safe configuration error that never includes certificate material."""


def absolute_path(value):
    if (not isinstance(value, str) or not re.fullmatch(r"/(?:[A-Za-z0-9_.-]+/)*[A-Za-z0-9_.-]+", value)
            or any(part in (".", "..") for part in value.split("/"))):
        raise SDSError("SDS paths must be normalized absolute POSIX paths")
    return PurePosixPath(value)


def file_path(source):
    if not isinstance(source, dict) or set(source) != {"filename"}:
        raise SDSError("Leaf certificates and keys require filename sources")
    return absolute_path(source["filename"])


def dynamic_leaf(leaf):
    if not isinstance(leaf, dict):
        raise SDSError("Leaf certificate must be a mapping")
    if set(leaf) - {"certificate_chain", "private_key", "watched_directory", "ocsp_staple", "signed_certificate_timestamp"}:
        raise SDSError("Unsupported leaf material for projected Secret rotation")
    certificate, key = file_path(leaf.get("certificate_chain")), file_path(leaf.get("private_key"))
    if certificate == key or certificate.parent != key.parent or certificate.parent == PurePosixPath("/"):
        raise SDSError("Certificate and key require distinct files in one projected Secret mount")
    watch = {"path": str(certificate.parent)}
    if "watched_directory" in leaf and leaf["watched_directory"] != watch:
        raise SDSError("watched_directory must be the projected Secret mount parent")
    for source in ([leaf["ocsp_staple"]] if "ocsp_staple" in leaf else []):
        file_path(source)
    for source in leaf.get("signed_certificate_timestamp", []):
        file_path(source)
    return {**deepcopy(leaf), "watched_directory": watch}


def transform_bootstrap(bootstrap, sds_directory="/etc/envoy-sds",
                        node_id="filesystem-sds-local", node_cluster="filesystem-sds"):
    """Return (independent bootstrap, {ConfigMap filename: SDS response dict}).

    TLS protocol floors, SAN matchers, client-certificate requirements, HTTP
    RBAC and all non-leaf configuration are copied unchanged. Duplicate leaf
    configurations share one content-addressed SDS resource across listeners
    and upstream clusters. Invalid/ambiguous source configurations fail closed.
    Envoy requires node.id and node.cluster even for local filesystem SDS.
    Missing node gets the explicit local defaults; an existing node must be
    complete and is preserved. This metadata is not an authorization identity.
    """
    directory = absolute_path(sds_directory)
    if not isinstance(bootstrap, dict) or not isinstance(bootstrap.get("static_resources"), dict):
        raise SDSError("Expected an Envoy bootstrap with static_resources")
    files = {}

    def convert_common(common):
        if not isinstance(common, dict):
            raise SDSError("common_tls_context must be a mapping")
        if "tls_certificates" not in common:
            return walk(common)
        if "tls_certificate_sds_secret_configs" in common:
            raise SDSError("Cannot mix static and dynamic leaf certificate sources")
        leaves = common["tls_certificates"]
        if not isinstance(leaves, list) or not leaves:
            raise SDSError("Static leaf certificate list must not be empty")
        references = []
        for leaf in leaves:
            material = dynamic_leaf(leaf)
            digest = hashlib.sha256(json.dumps(material, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
            name = "leaf-" + digest[:24]
            filename = name + ".json"
            files[filename] = {"version_info": digest, "resources": [
                {"@type": SECRET_TYPE, "name": name, "tls_certificate": material}]}
            references.append({"name": name, "sds_config": {
                "path_config_source": {"path": str(directory / filename), "watched_directory": {"path": str(directory)}},
                "resource_api_version": "V3",
            }})
        return {**{key: walk(value) for key, value in common.items() if key != "tls_certificates"},
                "tls_certificate_sds_secret_configs": references}

    def walk(value):
        if isinstance(value, dict):
            return {key: convert_common(item) if key == "common_tls_context" else walk(item)
                    for key, item in value.items()}
        if isinstance(value, list):
            return [walk(item) for item in value]
        return deepcopy(value)

    converted = walk(bootstrap)
    if files or uses_sds(converted):
        node = converted.get("node", {"id": node_id, "cluster": node_cluster})
        if not isinstance(node, dict) or any(
                not isinstance(node.get(key), str) or not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.:/~@-]{0,255}", node[key])
                for key in ("id", "cluster")):
            raise SDSError("Filesystem SDS requires valid node id and cluster")
        converted = {**converted, "node": deepcopy(node)}
    return converted, files


def uses_sds(value):
    if isinstance(value, dict):
        return bool(value.get("tls_certificate_sds_secret_configs")) or any(uses_sds(item) for item in value.values())
    if isinstance(value, list):
        return any(uses_sds(item) for item in value)
    return False
