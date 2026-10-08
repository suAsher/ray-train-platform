#!/usr/bin/env python3
"""Helm post-renderer, run on builder with PyYAML; retain the fixed chart names."""

import sys

import yaml

documents = list(yaml.safe_load_all(sys.stdin))
changed = 0
output = []
for document in documents:
    if document is None:
        continue
    if document.get("kind") in (
        "MutatingWebhookConfiguration", "ValidatingWebhookConfiguration"
    ):
        webhooks = []
        for hook in document.get("webhooks", []):
            service = hook.get("clientConfig", {}).get("service", {})
            if service.get("namespace") != "observability-alerting":
                raise SystemExit("unexpected CNPG webhook namespace")
            webhooks.append({
                **hook,
                "rules": [{**rule, "scope": "Namespaced"} for rule in hook.get("rules", [])],
                "namespaceSelector": {
                    "matchLabels": {
                        "kubernetes.io/metadata.name": "observability-alerting"
                    }
                },
            })
        document = {**document, "webhooks": webhooks}
        changed += 1
    output.append(document)
if changed != 2:
    raise SystemExit("expected exactly two CNPG webhook configurations")
yaml.safe_dump_all(output, sys.stdout, sort_keys=False)
