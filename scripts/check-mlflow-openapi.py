"""Validate the published MLflow contract on the build machine.

Validation dependencies are development tools only: openapi-spec-validator and
jsonschema. The shipped REST client itself uses only the Python standard library.
"""

import copy
import json
from pathlib import Path

from jsonschema import Draft202012Validator
from openapi_spec_validator import validate_spec


ROOT = Path(__file__).resolve().parents[1]


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError(f"duplicate JSON key: {key}")
        result[key] = value
    return result


def resolve(document, value):
    if "$ref" not in value:
        return value
    ref = value["$ref"]
    assert ref.startswith("#/"), "contract must not fetch external references"
    result = document
    for part in ref[2:].split("/"):
        result = result[part.replace("~1", "/").replace("~0", "~")]
    return result


def validator_for(document, schema):
    return Draft202012Validator({**schema, "components": document["components"]})


def main():
    path = ROOT / "docs/api/mlflow-integration.openapi.json"
    document = json.loads(path.read_text(encoding="utf-8"), object_pairs_hook=unique_object)
    validate_spec(document)
    assert document["openapi"] == "3.1.0"
    assert document["servers"][0]["url"] == "https://raytrain.wellspiking.ai"
    expected = {
        "/api/v1/experiments": "get",
        "/api/v1/jobs/{job_id}/experiment": "get",
        "/api/v1/jobs/{job_id}/mlflow/runs/{run_id}": "get",
        "/api/v1/jobs/{job_id}/mlflow/runs/{run_id}/log-batch": "post",
    }
    assert set(document["paths"]) == set(expected)
    examples = 0
    for route, method in expected.items():
        operation = document["paths"][route][method]
        assert operation["security"] == [{"bearerPAT": []}]
        contents = [resolve(document, value).get("content", {}) for value in operation["responses"].values()]
        if "requestBody" in operation:
            contents.append(operation["requestBody"]["content"])
        for content in contents:
            for media in content.values():
                validator = validator_for(document, media["schema"])
                for example in media.get("examples", {}).values():
                    validator.validate(example["value"])
                    examples += 1

    batch = validator_for(document, {"$ref": "#/components/schemas/MLflowLogBatch"})
    metric = {"key": "external/quality", "value": 0.87, "timestamp": 0, "step": 0}
    valid = [
        {"metrics": [metric]},
        {"params": [{"key": "external.version", "value": "v1"}]},
        {"tags": [{"key": "external.source", "value": "test"}]},
        {"metrics": [metric], "params": [], "tags": []},
    ]
    invalid = [{}, {"metrics": []}, {"metrics": None}, {"Metrics": [metric]},
               {"metrics": [metric], "run_id": "0" * 32}, {"metrics": [metric] * 101}]
    for field in ("key", "value", "timestamp", "step"):
        missing = copy.deepcopy(metric)
        del missing[field]
        invalid.append({"metrics": [missing]})
    for field, value in (("timestamp", -1), ("timestamp", 253402300800000),
                         ("step", -1), ("step", 9223372036854775808),
                         ("step", 0.5), ("value", None), ("extra", 1)):
        invalid.append({"metrics": [{**metric, field: value}]})
    for payload in valid:
        batch.validate(payload)
    for payload in invalid:
        assert not batch.is_valid(payload), f"invalid batch accepted: {payload}"

    run_id = validator_for(document, {"$ref": "#/components/schemas/RunID"})
    run_id.validate("0123456789abcdef0123456789abcdef")
    for value in ("../admin", "A" * 32, "0" * 31, "0" * 33):
        assert not run_id.is_valid(value)
    print(f"PASS: OpenAPI 3.1, four routes, {examples} examples, {len(valid)} valid and {len(invalid)} invalid batches, Run ID bounds")


if __name__ == "__main__":
    main()
