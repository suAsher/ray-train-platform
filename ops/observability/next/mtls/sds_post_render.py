#!/usr/bin/env python3
"""Scope-limited Helm/Kustomize post-renderer for Envoy filesystem SDS.

Read Kubernetes YAML from stdin; emit the complete transformed stream only once
all documents pass. Use --namespace for Helm documents that omit metadata.ns.
This changes only the known proxy ConfigMaps and their volume.items lists.
"""

import argparse
from copy import deepcopy
import json
from pathlib import PurePosixPath
import re
import sys

import yaml

from sds import SDSError, transform_bootstrap


NAMESPACES = frozenset(("victoriametrics", "clickhouse", "opentelemetry", "nightingale"))
MAPPINGS = {
    **{("victoriametrics", "victoriametrics-cluster-" + role + "-mtls"): ("envoy.yaml", "/etc/envoy-source")
       for role in ("vmstorage", "vminsert", "vmselect", "vmauth", "vmagent", "vmalert", "alertmanager", "operator")},
    **{("clickhouse", "clickhouse-proxy-" + role): ("proxy-" + role + ".yaml", "/etc/envoy-source")
       for role in ("clickhouse", "keeper", "operator")},
    ("opentelemetry", "otel-gateway-proxy"): ("proxy-otel.yaml", "/etc/envoy-template"),
    ("nightingale", "n9e-config-v911"): ("envoy.yaml", "/etc/envoy"),
    ("nightingale", "cloudnative-pg-operator-mtls"): ("envoy.yaml", "/etc/envoy"),
}
IDENTITY_DIRECTORY = "/etc/obs-mtls"


class RenderError(ValueError):
    """Never includes manifest bodies or credential values."""


def optional_mapping(value, field):
    if value is None:
        return {}
    if not isinstance(value, dict):
        raise RenderError("Invalid optional mapping: " + field)
    return value


def optional_list(value, field):
    if value is None:
        return []
    if not isinstance(value, list) or any(not isinstance(item, dict) for item in value):
        raise RenderError("Invalid optional object list: " + field)
    return value


def pod_specs(value):
    """Find standard and Operator CR PodSpecs without assuming CRD nesting."""
    if isinstance(value, dict):
        if "containers" in value and "volumes" in value:
            yield value
        for item in value.values():
            yield from pod_specs(item)
    elif isinstance(value, list):
        for item in value:
            yield from pod_specs(item)


def decode(text):
    try:
        document = yaml.safe_load(text)
    except (yaml.YAMLError, TypeError, ValueError):
        raise RenderError("Invalid proxy configuration document") from None
    if not isinstance(document, dict):
        raise RenderError("Proxy configuration must be a mapping")
    return document


def check_leaf(leaf):
    if not isinstance(leaf, dict) or any(leaf.get(key) != {"filename": IDENTITY_DIRECTORY + "/" + filename}
                                       for key, filename in (("certificate_chain", "tls.crt"), ("private_key", "tls.key"))):
        raise RenderError("Unrecognized TLS filename or inline key material")


def restore_dynamic(common, data, directory):
    """Reconstruct our own validated dynamic leaves for idempotent conversion."""
    if "tls_certificate_sds_secret_configs" not in common:
        return common
    if "tls_certificates" in common:
        raise RenderError("Mixed static and dynamic leaf sources")
    references = common["tls_certificate_sds_secret_configs"]
    if not isinstance(references, list) or not references:
        raise RenderError("Invalid SDS reference list")
    leaves = []
    for reference in references:
        name = reference.get("name") if isinstance(reference, dict) else None
        if not isinstance(name, str) or not re.fullmatch(r"leaf-[0-9a-f]{24}", name):
            raise RenderError("Unrecognized SDS resource name")
        filename = name + ".json"
        expected = {"name": name, "sds_config": {"path_config_source": {
            "path": str(PurePosixPath(directory) / filename), "watched_directory": {"path": directory}},
            "resource_api_version": "V3"}}
        if reference != expected or filename not in data:
            raise RenderError("Unrecognized SDS path or missing ConfigMap key")
        response = decode(data[filename])
        resources = response.get("resources", [])
        if len(resources) != 1 or resources[0].get("name") != name or "tls_certificate" not in resources[0]:
            raise RenderError("Invalid SDS discovery response")
        leaves.append(resources[0]["tls_certificate"])
    return {**{key: value for key, value in common.items() if key != "tls_certificate_sds_secret_configs"}, "tls_certificates": leaves}


def restore_bootstrap(value, data, directory):
    if isinstance(value, list):
        return [restore_bootstrap(item, data, directory) for item in value]
    if not isinstance(value, dict):
        return deepcopy(value)
    result = {}
    for key, item in value.items():
        if key == "common_tls_context":
            if not isinstance(item, dict):
                raise RenderError("Invalid common TLS context")
            common = restore_dynamic(item, data, directory)
            for leaf in common.get("tls_certificates", []):
                check_leaf(leaf)
            ca = common.get("validation_context", {}).get("trusted_ca")
            if ca is not None and ca != {"filename": IDENTITY_DIRECTORY + "/ca.crt"}:
                raise RenderError("Unrecognized TLS trust filename")
            result[key] = common
        else:
            result[key] = restore_bootstrap(item, data, directory)
    return result


def transform_configmap(document, mapping, namespace):
    key, directory = mapping
    data = optional_mapping(document.get("data"), "ConfigMap.data")
    if key not in data or not isinstance(data[key], str):
        raise RenderError("Known Envoy ConfigMap is missing its bootstrap key")
    bootstrap = restore_bootstrap(decode(data[key]), data, directory)
    try:
        converted, files = transform_bootstrap(bootstrap, directory,
                                               node_id=namespace + "." + document["metadata"]["name"], node_cluster=namespace)
    except SDSError:
        raise RenderError("Envoy leaf configuration failed SDS conversion") from None
    if not files:
        raise RenderError("Known Envoy ConfigMap has no file-backed leaf identity")
    extras = {}
    for filename, response in files.items():
        if filename in optional_mapping(document.get("binaryData"), "ConfigMap.binaryData"):
            raise RenderError("SDS ConfigMap binaryData key collision")
        if filename in data:
            try:
                matches = decode(data[filename]) == response
            except RenderError:
                matches = False
            if not matches:
                raise RenderError("SDS ConfigMap key collision")
        extras[filename] = json.dumps(response, sort_keys=True, separators=(",", ":"))
    if any(name.startswith("leaf-") and name not in files for name in data):
        raise RenderError("Unexpected SDS ConfigMap key collision")
    new_data = {**data, key: json.dumps(converted, sort_keys=True, indent=2), **extras}
    return {**document, "data": new_data}, tuple(sorted(files))


def check_items(items, required):
    if not isinstance(items, list) or any(not isinstance(item, dict) for item in items):
        raise RenderError("Invalid volume.items whitelist")
    keys = [item.get("key") for item in items]
    paths = [item.get("path") for item in items]
    if len(set(keys)) != len(keys) or len(set(paths)) != len(paths):
        raise RenderError("Duplicate volume.items whitelist")
    if any(item.get("path") in required and item.get("key") != item.get("path") for item in items):
        raise RenderError("ConfigMap item path collision")
    for key in required:
        item = next((item for item in items if item.get("key") == key), None)
        if item is not None and item.get("path") != key:
            raise RenderError("Remapped Envoy or TLS volume.items path")


def validate_identity(volume):
    source = volume.get("secret")
    if not isinstance(source, dict) or not str(source.get("secretName", "")).startswith("obs-mtls-"):
        raise RenderError("Envoy identity must be a known directory-mounted Secret")
    if "items" in source:
        expected = ("tls.crt", "tls.key", "ca.crt")
        check_items(source["items"], expected)
        if not set(expected).issubset(item["key"] for item in source["items"]):
            raise RenderError("TLS Secret items omit required identity files")


def validate_envoy(container, volumes, namespace):
    mounts = optional_list(container.get("volumeMounts"), "container.volumeMounts")
    names = [item.get("name") for item in mounts]
    if len(set(names)) != len(names):
        raise RenderError("Duplicate Envoy volume mount")
    found_config, found_identity = False, False
    for mount in mounts:
        volume = volumes.get(mount.get("name"), {})
        config_name = optional_mapping(volume.get("configMap"), "volume.configMap").get("name")
        config_key = (namespace, config_name)
        is_config = config_name is not None
        is_identity = mount.get("mountPath") == IDENTITY_DIRECTORY
        if not (is_config or is_identity):
            continue
        if "subPath" in mount or "subPathExpr" in mount:
            raise RenderError("Envoy SDS cannot use subPath mounts")
        if mount.get("readOnly") is not True:
            raise RenderError("Envoy identity and config mounts must be read-only")
        if is_config:
            if config_key not in MAPPINGS or mount.get("mountPath") != MAPPINGS[config_key][1]:
                raise RenderError("Unrecognized Envoy config mount")
            found_config = True
        if is_identity:
            validate_identity(volume)
            found_identity = True
    if not found_config or not found_identity:
        raise RenderError("Envoy is missing required config or Secret mount")


def transform_pod_spec(specification, namespace, generated):
    volumes = optional_list(specification.get("volumes"), "pod.volumes")
    named = {volume.get("name"): volume for volume in volumes}
    if len(named) != len(volumes):
        raise RenderError("Duplicate Pod volume name")
    for volume in volumes:
        source = optional_mapping(volume.get("configMap"), "volume.configMap")
        key = (namespace, source.get("name"))
        if key not in MAPPINGS or "items" not in source:
            continue
        if key not in generated:
            raise RenderError("Whitelisted Envoy ConfigMap must occur in the same input")
        required = (MAPPINGS[key][0], *generated[key])
        check_items(source["items"], required)
        if not any(item.get("key") == required[0] for item in source["items"]):
            raise RenderError("ConfigMap items omit Envoy bootstrap")
        present = {item["key"] for item in source["items"]}
        source["items"] = [*source["items"], *({"key": name, "path": name} for name in generated[key] if name not in present)]
    for container in [*optional_list(specification.get("containers"), "pod.containers"),
                      *optional_list(specification.get("initContainers"), "pod.initContainers")]:
        if container.get("name") in ("envoy", "mtls-proxy") or "envoyproxy/envoy:" in container.get("image", ""):
            validate_envoy(container, named, namespace)


def transform_documents(documents, namespace=None):
    if namespace is not None and namespace not in NAMESPACES:
        raise RenderError("Default namespace is outside product scope")
    if not isinstance(documents, list) or any(item is not None and not isinstance(item, dict) for item in documents):
        raise RenderError("Expected a Kubernetes document stream")
    output, generated = [], {}
    for original in documents:
        if original is None:
            continue
        document = deepcopy(original)
        metadata = document.get("metadata", {})
        scope, name = metadata.get("namespace", namespace), metadata.get("name")
        key = (scope, name)
        if document.get("kind") == "ConfigMap":
            if scope is None and any(name == item[1] for item in MAPPINGS):
                raise RenderError("Known ConfigMap requires an explicit namespace context")
            if key in MAPPINGS:
                if key in generated:
                    raise RenderError("Duplicate known Envoy ConfigMap")
                document, generated[key] = transform_configmap(document, MAPPINGS[key], scope)
            elif scope in NAMESPACES and any(isinstance(text, str) and ("tls_certificates" in text or "tls_certificate_sds_secret_configs" in text)
                                            for text in optional_mapping(document.get("data"), "ConfigMap.data").values()):
                raise RenderError("unrecognized TLS ConfigMap in product namespace")
        output.append(document)
    for document in output:
        scope = document.get("metadata", {}).get("namespace", namespace)
        if scope in NAMESPACES:
            for specification in pod_specs(document):
                transform_pod_spec(specification, scope, generated)
    return output


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--namespace", choices=sorted(NAMESPACES))
    args = parser.parse_args()
    try:
        documents = list(yaml.safe_load_all(sys.stdin.read()))
        output = transform_documents(documents, args.namespace)
    except RenderError as error:
        print("SDS post-render validation failed: " + str(error) + "; no manifests emitted", file=sys.stderr)
        return 1
    except (yaml.YAMLError, TypeError, KeyError, AttributeError):
        print("SDS post-render validation failed; no manifests emitted", file=sys.stderr)
        return 1
    sys.stdout.write(yaml.safe_dump_all(output, sort_keys=False))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
