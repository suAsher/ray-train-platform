#!/usr/bin/env python3
"""Make the fixed Redis chart's namespace-local ServiceAccount binding explicit."""

import sys

import yaml

documents = list(yaml.safe_load_all(sys.stdin))
output = []
checked_bindings = 0
for document in documents:
    if document is None:
        continue
    metadata = document.get("metadata", {})
    if metadata.get("namespace") != "observability-alerting":
        raise SystemExit("unexpected Redis object namespace")
    if document.get("kind") == "RoleBinding" and metadata.get("name") == "n9e-redis":
        subjects = []
        for subject in document.get("subjects", []):
            if (
                subject.get("kind") != "ServiceAccount"
                or subject.get("name") != "n9e-redis"
                or subject.get("namespace", "observability-alerting") != "observability-alerting"
            ):
                raise SystemExit("unexpected Redis RoleBinding subject")
            subjects.append({**subject, "namespace": "observability-alerting"})
        if len(subjects) != 1:
            raise SystemExit("expected one Redis ServiceAccount binding")
        document = {**document, "subjects": subjects}
        checked_bindings += 1
    output.append(document)
if checked_bindings != 1:
    raise SystemExit("expected exactly one Redis RoleBinding")
yaml.safe_dump_all(output, sys.stdout, sort_keys=False)
