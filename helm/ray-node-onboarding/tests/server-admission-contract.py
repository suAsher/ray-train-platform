#!/usr/bin/env python3
"""Exercise installed admission policies using server dry-runs only.

Fixtures must come from backend/nodeonboarding TestAdmissionFixtures with actual
Node identity and chart image/NFS values. No object is created, patched or
deleted persistently; no Pod is scheduled and no GPU is requested.
"""

import argparse
import copy
import json
import pathlib
import subprocess
import sys


NAMESPACE = "ray-cache-local"
CONTROLLER = f"system:serviceaccount:{NAMESPACE}:node-onboarding"
POLICIES = ["nodes", "configmaps", "owned-resources", "pods", "pvcs", "leases", "state", "ready-label"]
READY = "platform.wellspiking.ai/cache-ready"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("fixture_dir", type=pathlib.Path)
    parser.add_argument("--kubectl", default="kubectl")
    parser.add_argument("--context", help="Explicit existing kubectl context")
    parser.add_argument("--data1-config-map", default="ray-cache-local-data1-runtime")
    parser.add_argument("--data2-config-map", default="ray-cache-local-data2-runtime")
    parser.add_argument("--provisioner-service-account", default="ray-cache-local-data1")
    args = parser.parse_args()
    base = [args.kubectl, "--request-timeout=30s"]
    if args.context:
        base += ["--context", args.context]
    passed = []

    def call(argv, obj=None, as_controller=True):
        command = base + ([f"--as={CONTROLLER}"] if as_controller else []) + argv
        # Defense against accidental future additions of a persistent mutation.
        if argv[0] != "get" and "--dry-run=server" not in argv:
            raise RuntimeError("Every mutation must be a server dry-run")
        return subprocess.run(command, input=None if obj is None else json.dumps(obj),
                              text=True, capture_output=True, check=False)

    def get(kind, name=None, namespace=None):
        command = ["get", kind] + ([name] if name else [])
        if namespace:
            command += ["-n", namespace]
        result = call(command + ["-o", "json"], as_controller=False)
        if result.returncode:
            raise RuntimeError(result.stderr.strip())
        return json.loads(result.stdout)

    def check(name, argv, obj=None, denied_by=None, as_controller=True):
        result = call(argv, obj, as_controller)
        output = result.stdout + result.stderr
        if denied_by:
            if result.returncode == 0 or f"node-onboarding-{denied_by}" not in output:
                raise RuntimeError(f"{name}: expected denial by {denied_by}, got {output}")
        elif result.returncode:
            raise RuntimeError(f"{name}: expected allow, got {output}")
        passed.append(name)
        print(f"PASS {name}", flush=True)

    def create(name, obj, denied_by=None, as_controller=True):
        check(name, ["create", "--dry-run=server", "-f", "-", "-o", "json"], obj,
              denied_by, as_controller)

    def replace(name, obj, denied_by=None, as_controller=True):
        check(name, ["replace", "--dry-run=server", "-f", "-", "-o", "json"], obj,
              denied_by, as_controller)

    def mutate(original, path, value):
        obj = copy.deepcopy(original)
        target = obj
        for key in path[:-1]:
            target = target[key]
        target[path[-1]] = value
        return obj

    for suffix in POLICIES:
        policy = get("validatingadmissionpolicy", f"node-onboarding-{suffix}")
        status = policy.get("status", {})
        if status.get("observedGeneration") != policy["metadata"]["generation"]:
            raise RuntimeError(f"Policy {suffix} has not observed its current generation")
        if "typeChecking" not in status or status["typeChecking"].get("expressionWarnings"):
            raise RuntimeError(f"Policy {suffix} has missing type checking or warnings: {status}")
        binding = get("validatingadmissionpolicybinding", f"node-onboarding-{suffix}")
        if binding["spec"].get("policyName") != f"node-onboarding-{suffix}" or "Deny" not in binding["spec"].get("validationActions", []):
            raise RuntimeError(f"Policy {suffix} is not bound with Deny")

    fixtures = {name: json.loads((args.fixture_dir / f"{name}.json").read_text())
                for name in ["prepare-pod", "probe-pod", "cache1-pvc", "cache2-pvc"]}
    prep, probe = fixtures["prepare-pod"], fixtures["probe-pod"]
    node_name = prep["metadata"]["ownerReferences"][0]["name"]
    node = get("node", node_name)
    if node["metadata"]["uid"] != prep["metadata"]["ownerReferences"][0]["uid"]:
        raise RuntimeError("Fixture Node UID is stale; regenerate fixtures")
    for name, obj in fixtures.items():
        if obj["metadata"]["namespace"] != NAMESPACE:
            raise RuntimeError("Fixture namespace must be ray-cache-local")
        create(f"exact {name}", obj)

    pod_mutations = [
        ("mutable image", ["spec", "containers", 0, "image"], "registry.example/unsafe:latest"),
        ("altered command", ["spec", "containers", 0, "command"], ["/bin/sh", "-ec", "true"]),
        ("extra args", ["spec", "containers", 0, "args"], ["unexpected"]),
        ("env injection", ["spec", "containers", 0, "env"], [{"name": "ENV", "value": "/data1/payload"}]),
        ("privileged", ["spec", "containers", 0, "securityContext", "privileged"], True),
        ("extra capability", ["spec", "containers", 0, "securityContext", "capabilities", "add"], ["SYS_ADMIN"]),
        ("SA token", ["spec", "automountServiceAccountToken"], True),
        ("host PID", ["spec", "hostPID"], True),
        ("host network", ["spec", "hostNetwork"], True),
        ("preemption", ["spec", "preemptionPolicy"], "PreemptLowerPriority"),
        ("alternate scheduler", ["spec", "schedulerName"], "unsafe-scheduler"),
        ("host DNS", ["spec", "dnsPolicy"], "Default"),
        ("custom DNS", ["spec", "dnsConfig"], {"nameservers": ["192.0.2.1"]}),
        ("host aliases", ["spec", "hostAliases"], [{"ip": "192.0.2.1", "hostnames": ["nfs.example"]}]),
        ("host root", ["spec", "volumes", 0, "hostPath", "path"], "/"),
        ("writable sysfs", ["spec", "containers", 0, "volumeMounts", 3, "readOnly"], False),
    ]
    for label, path, value in pod_mutations:
        create(label, mutate(prep, path, value), "pods")
    # Give the extra container a unique name so admission, not core API schema,
    # is the reason for denial in the separate sidecar case.
    sidecar = copy.deepcopy(prep)
    sidecar["spec"]["containers"].append(dict(copy.deepcopy(prep["spec"]["containers"][1]), name="extra"))
    create("unique sidecar", sidecar, "pods")
    nfs_index = next(i for i, v in enumerate(probe["spec"]["volumes"]) if "nfs" in v)
    create("writable NFS", mutate(probe, ["spec", "volumes", nfs_index, "nfs", "readOnly"], False), "pods")
    create("unowned probe", mutate(prep, ["metadata", "labels"], {}), "owned-resources")
    create("unrelated PVC class", mutate(fixtures["cache1-pvc"], ["spec", "storageClassName"], "unrelated-class"), "pvcs")

    # A non-controller provisioner keeps its normal Pod creation permission.
    other = copy.deepcopy(probe)
    other["metadata"] = {"name": "onboarding-policy-scope-check", "namespace": NAMESPACE}
    other["spec"].pop("affinity", None)
    other["spec"].pop("nodeSelector", None)
    other["spec"]["serviceAccountName"] = args.provisioner_service_account
    other["spec"]["volumes"] = []
    other["spec"]["containers"] = [copy.deepcopy(prep["spec"]["containers"][1])]
    check("existing provisioner requester unaffected",
          [f"--as=system:serviceaccount:{NAMESPACE}:{args.provisioner_service_account}",
           "create", "--dry-run=server", "-f", "-", "-o", "json"], other, as_controller=False)

    def node_patch(name, operations, denied_by=None, as_controller=True):
        latest = get("node", node_name)
        preconditions = [{"op": "test", "path": "/metadata/uid", "value": node["metadata"]["uid"]},
                         {"op": "test", "path": "/metadata/resourceVersion", "value": latest["metadata"]["resourceVersion"]}]
        check(name, ["patch", "node", node_name, "--type=json", "--patch", json.dumps(preconditions + operations),
                     "--dry-run=server", "-o", "json"], denied_by=denied_by, as_controller=as_controller)

    labels = dict(node["metadata"].get("labels", {}), **{READY: "true"})
    node_patch("controller ready label", [{"op": "add", "path": "/metadata/labels", "value": labels}])
    labels[READY] = "false" if node["metadata"].get("labels", {}).get(READY) == "true" else "true"
    node_patch("other requester cannot forge ready", [{"op": "add", "path": "/metadata/labels", "value": labels}], "ready-label", False)
    cordon = [{"op": "add", "path": "/spec/unschedulable", "value": not node.get("spec", {}).get("unschedulable", False)}]
    node_patch("controller cannot cordon", cordon, "nodes")
    node_patch("normal operator cordon unaffected", cordon, as_controller=False)
    for field, value in [("labels", dict(node["metadata"].get("labels", {}), **{"unrelated.example/test": "changed"})),
                         ("annotations", dict(node["metadata"].get("annotations", {}), **{"unrelated.example/test": "changed"})),
                         ("finalizers", ["unrelated.example/hold"])]:
        node_patch(f"controller preserves foreign {field}", [{"op": "add", "path": f"/metadata/{field}", "value": value}], "nodes")
    create("normal new Node allowed", {"apiVersion": "v1", "kind": "Node", "metadata": {"name": "onboarding-policy-new-node"}}, as_controller=False)
    create("new Node cannot claim readiness", {"apiVersion": "v1", "kind": "Node", "metadata": {"name": "onboarding-policy-new-node", "labels": {READY: "true"}}}, "ready-label", False)

    lease_result = call(["get", "lease", "node-onboarding", "-n", NAMESPACE, "--ignore-not-found", "-o", "json"], as_controller=False)
    if lease_result.returncode:
        raise RuntimeError(lease_result.stderr)
    if lease_result.stdout.strip():
        lease = json.loads(lease_result.stdout)
        replace("own leader lease allowed", lease)
        released = copy.deepcopy(lease)
        released.setdefault("spec", {}).update({"holderIdentity": "", "leaseDurationSeconds": 1})
        replace("own leader lease release allowed", released)
    else:
        create("own leader lease allowed", {"apiVersion": "coordination.k8s.io/v1", "kind": "Lease",
               "metadata": {"name": "node-onboarding", "namespace": NAMESPACE}, "spec": {"holderIdentity": "admission-check"}})
        print("NOTE: Lease is absent; CREATE checked. Rerun after the own Lease exists to exercise UPDATE/release dry-runs.", flush=True)
    create("foreign leader lease denied", {"apiVersion": "coordination.k8s.io/v1", "kind": "Lease",
           "metadata": {"name": "onboarding-policy-foreign", "namespace": NAMESPACE}, "spec": {}}, "leases")

    for name in [args.data1_config_map, args.data2_config_map]:
        cm = get("configmap", name, NAMESPACE)
        replace(f"runtime config.json allowed: {name}", cm)
        bad = copy.deepcopy(cm)
        bad["data"]["setup"] = bad["data"].get("setup", "") + "\n# forbidden controller script edit"
        replace(f"runtime scripts protected: {name}", bad, "configmaps")
    state = get("configmap", "node-onboarding-state", NAMESPACE)
    proof = copy.deepcopy(state)
    proof["data"] = {"state.json": '{"admission-fixture":"not-a-real-proof"}'}
    replace("controller proof-store write", proof)
    replace("operator proof forgery denied", proof, "state", False)
    check("proof-store deletion denied", ["delete", "configmap", "node-onboarding-state", "-n", NAMESPACE,
          "--dry-run=server", "--wait=false"], denied_by="state", as_controller=False)
    pods = get("pods", namespace=NAMESPACE)["items"]
    foreign = next((p for p in pods if not p["metadata"]["name"].startswith("onboard-")), None)
    if foreign is None:
        raise RuntimeError("Need an existing non-probe Pod to test protected deletion by dry-run")
    check("unowned Pod deletion denied", ["delete", "pod", foreign["metadata"]["name"], "-n", NAMESPACE,
          "--dry-run=server", "--wait=false"], denied_by="owned-resources")
    print(f"All {len(passed)} server admission checks passed; no changes persisted.")


if __name__ == "__main__":
    try:
        main()
    except (RuntimeError, OSError, KeyError, ValueError, StopIteration) as exc:
        print(f"FAIL: {exc}", file=sys.stderr)
        sys.exit(1)
