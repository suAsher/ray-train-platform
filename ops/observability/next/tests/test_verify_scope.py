"""Publication boundary tests. Run on the builder, never against a cluster."""

import copy
from contextlib import redirect_stderr
import importlib.util
import io
from pathlib import Path
import tempfile
import unittest


MODULE_PATH = Path(__file__).resolve().parents[1] / "verify_scope.py"
SPEC = importlib.util.spec_from_file_location("verify_scope", MODULE_PATH)
scope = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(scope)


def resources():
    return {"requests": {"cpu": "100m", "memory": "128Mi"},
            "limits": {"cpu": "1", "memory": "256Mi"}}


def pod_spec():
    return {"priorityClassName": scope.PRIORITY_CLASS,
            "containers": [{"name": "app", "image": "example.invalid/app:1",
                            "resources": resources()}]}


def workload(kind="Deployment", replicas=1):
    return {"apiVersion": "apps/v1", "kind": kind,
            "metadata": {"name": "canary", "namespace": "observability-otel"},
            "spec": {"replicas": replicas,
                     "template": {"metadata": {"labels": {"app": "canary"}},
                                  "spec": pod_spec()}}}


def anti_affinity(hard=True):
    term = {"topologyKey": "kubernetes.io/hostname",
            "labelSelector": {"matchLabels": {"app": "canary"}}}
    mode = "requiredDuringSchedulingIgnoredDuringExecution" if hard else "preferredDuringSchedulingIgnoredDuringExecution"
    return {"podAntiAffinity": {mode: [term if hard else {"weight": 100, "podAffinityTerm": term}]}}


def claim(size="20Gi"):
    return {"metadata": {"name": "data"}, "spec": {
        "storageClassName": scope.STORAGE_CLASS, "accessModes": ["ReadWriteOnce"],
        "resources": {"requests": {"storage": size}}}}


def webhook():
    return {"apiVersion": "admissionregistration.k8s.io/v1",
            "kind": "ValidatingWebhookConfiguration", "metadata": {"name": "n9e-cnpg"},
            "webhooks": [{"name": "vcluster.postgresql.cnpg.io",
                          "namespaceSelector": {"matchLabels": {
                              "kubernetes.io/metadata.name": "observability-alerting"}},
                          "clientConfig": {"service": {"name": "n9e-cnpg-webhook-service",
                                                       "namespace": "observability-alerting"}},
                          "rules": [{"apiGroups": ["postgresql.cnpg.io"],
                                     "apiVersions": ["v1"], "operations": ["CREATE", "UPDATE"],
                                     "resources": ["clusters"], "scope": "Namespaced"}]}]}


class ScopeTests(unittest.TestCase):
    def errors(self, document, allow=()):
        return scope.validate_documents([document], allow).errors

    def assert_rejected(self, document, fragment, allow=()):
        self.assertTrue(any(fragment in error for error in self.errors(document, allow)))

    def test_valid_additive_workload_and_secret_reference(self):
        doc = workload()
        doc["spec"]["template"]["spec"]["containers"][0]["env"] = [{
            "name": "PASSWORD", "valueFrom": {"secretKeyRef": {"name": "runtime-auth", "key": "password"}}}]
        self.assertEqual([], self.errors(doc))

    def test_optional_pod_lists_rendered_as_null_are_empty(self):
        doc = workload()
        pod = doc["spec"]["template"]["spec"]
        for field in ("volumes", "initContainers", "ephemeralContainers", "tolerations", "topologySpreadConstraints"):
            pod[field] = None
        pod["containers"][0]["env"] = None
        pod["containers"][0]["ports"] = None
        self.assertEqual([], self.errors(doc))

    def test_old_or_implicit_namespace_is_rejected(self):
        for namespace in ("monitoring", "loki", "ray-train-platform", None):
            with self.subTest(namespace=namespace):
                doc = workload()
                doc["metadata"]["namespace"] = namespace
                self.assert_rejected(doc, "namespace")

    def test_gpu_requests_and_limits_rejected_even_zero(self):
        for resource in ("nvidia.com/gpu", "nvidia.com/mig-1g.5gb", "amd.com/gpu"):
            for bucket in ("requests", "limits"):
                doc = workload()
                doc["spec"]["template"]["spec"]["containers"][0]["resources"][bucket][resource] = "0"
                self.assert_rejected(doc, "GPU")

    def test_high_privilege_and_host_access_rejected(self):
        for field in ("hostNetwork", "hostPID", "hostIPC"):
            doc = workload()
            doc["spec"]["template"]["spec"][field] = True
            self.assert_rejected(doc, field)
        doc = workload()
        pod = doc["spec"]["template"]["spec"]
        pod["volumes"] = [{"name": "host", "hostPath": {"path": "/"}}]
        self.assert_rejected(doc, "hostPath")
        pod.pop("volumes")
        pod["containers"][0]["securityContext"] = {"privileged": True}
        self.assert_rejected(doc, "privileged")

    def test_init_container_requires_budget_and_no_host_port(self):
        doc = workload()
        pod = doc["spec"]["template"]["spec"]
        pod["initContainers"] = [{"name": "init", "image": "example.invalid/init:1"}]
        self.assert_rejected(doc, "requests.cpu")
        pod.pop("initContainers")
        pod["containers"][0]["ports"] = [{"containerPort": 80, "hostPort": 80}]
        self.assert_rejected(doc, "hostPort")

    def test_priority_and_preemption_required(self):
        for value in (None, "system-node-critical", "default"):
            doc = workload()
            doc["spec"]["template"]["spec"]["priorityClassName"] = value
            self.assert_rejected(doc, "priorityClassName")
        pc = {"apiVersion": "scheduling.k8s.io/v1", "kind": "PriorityClass",
              "metadata": {"name": scope.PRIORITY_CLASS}, "value": -1000,
              "globalDefault": False, "preemptionPolicy": "Never"}
        self.assertEqual([], self.errors(pc))
        pc["preemptionPolicy"] = "PreemptLowerPriority"
        self.assert_rejected(pc, "preemptionPolicy")

    def test_dedicated_algorithm_and_wildcard_tolerations_rejected(self):
        for toleration in ({"key": scope.DEDICATED_TAINT, "value": "algorithm", "effect": "NoSchedule"},
                           {"key": scope.DEDICATED_TAINT, "operator": "Exists"},
                           {"operator": "Exists"}):
            doc = workload()
            doc["spec"]["template"]["spec"]["tolerations"] = [toleration]
            self.assert_rejected(doc, "dedicated")

    def test_cpu_only_selector_rejected_but_os_selector_allowed(self):
        doc = workload()
        doc["spec"]["template"]["spec"]["nodeSelector"] = {"kubernetes.io/os": "linux"}
        self.assertEqual([], self.errors(doc))
        doc["spec"]["template"]["spec"]["nodeSelector"] = {"workload": "cpu"}
        self.assert_rejected(doc, "CPU-only")

    def test_public_services_and_ingress_rejected(self):
        for service_type in ("LoadBalancer", "NodePort", "ExternalName"):
            doc = {"apiVersion": "v1", "kind": "Service", "metadata": {
                "name": "app", "namespace": "observability-otel"}, "spec": {"type": service_type}}
            self.assert_rejected(doc, "ClusterIP")
        doc["kind"] = "Ingress"
        self.assert_rejected(doc, "Ingress")

    def test_secret_data_rejected_without_disclosing_payload(self):
        for field in ("data", "stringData"):
            doc = {"apiVersion": "v1", "kind": "Secret", "metadata": {
                "name": "runtime-only", "namespace": "observability-otel"}, field: {"password": "DO-NOT-ECHO"}}
            errors = self.errors(doc)
            self.assertTrue(errors)
            self.assertNotIn("DO-NOT-ECHO", repr(errors))

    def test_storage_class_contract(self):
        sc = {"apiVersion": "storage.k8s.io/v1", "kind": "StorageClass",
              "metadata": {"name": scope.STORAGE_CLASS}, "provisioner": "ebs.csi.volcengine.com",
              "parameters": {"type": "ESSD_PL0"}, "reclaimPolicy": "Retain",
              "volumeBindingMode": "WaitForFirstConsumer", "allowVolumeExpansion": True}
        self.assertEqual([], self.errors(sc))
        for field, value in (("reclaimPolicy", "Delete"), ("volumeBindingMode", "Immediate"),
                             ("parameters", {"type": "ESSD_PL1"})):
            bad = copy.deepcopy(sc)
            bad[field] = value
            self.assertTrue(self.errors(bad))

    def test_ebs_minimum_and_storage_class(self):
        for size in ("19Gi", "20G", "0", "unparseable"):
            doc = workload("StatefulSet")
            doc["spec"]["volumeClaimTemplates"] = [claim(size)]
            self.assert_rejected(doc, "20Gi")
        doc["spec"]["volumeClaimTemplates"] = [claim()]
        doc["spec"]["volumeClaimTemplates"][0]["spec"]["storageClassName"] = "ebs-ssd"
        self.assert_rejected(doc, "storageClass")

    def test_persistent_replicas_require_hard_valid_matching_anti_affinity(self):
        doc = workload("StatefulSet", 2)
        doc["spec"]["volumeClaimTemplates"] = [claim()]
        pod = doc["spec"]["template"]["spec"]
        self.assert_rejected(doc, "anti-affinity")
        pod["affinity"] = anti_affinity(False)
        self.assert_rejected(doc, "anti-affinity")
        pod["affinity"] = anti_affinity()
        self.assertEqual([], self.errors(doc))
        term = pod["affinity"]["podAntiAffinity"]["requiredDuringSchedulingIgnoredDuringExecution"][0]
        term["labelSelector"]["matchLabels"]["app"] = "other"
        self.assert_rejected(doc, "anti-affinity")

    def test_stateless_soft_anti_affinity_is_review_warning(self):
        doc = workload(replicas=2)
        doc["spec"]["template"]["spec"]["affinity"] = anti_affinity(False)
        report = scope.validate_documents([doc])
        self.assertEqual([], report.errors)
        self.assertTrue(any("preferred" in item for item in report.warnings))

    def test_cron_job_template_is_checked(self):
        doc = {"apiVersion": "batch/v1", "kind": "CronJob", "metadata": {
            "name": "canary", "namespace": "observability-otel"}, "spec": {"jobTemplate": {
                "spec": {"template": {"spec": pod_spec()}}}}}
        doc["spec"]["jobTemplate"]["spec"]["template"]["spec"]["hostNetwork"] = True
        self.assert_rejected(doc, "hostNetwork")

    def test_crds_need_exact_allowlist_and_approved_group(self):
        doc = {"apiVersion": "apiextensions.k8s.io/v1", "kind": "CustomResourceDefinition",
               "metadata": {"name": "vmclusters.operator.victoriametrics.com"}, "spec": {
                   "group": "operator.victoriametrics.com", "scope": "Namespaced",
                   "names": {"plural": "vmclusters", "kind": "VMCluster"}}}
        self.assert_rejected(doc, "allowlist")
        allowed = ["CustomResourceDefinition/" + doc["metadata"]["name"]]
        self.assertEqual([], self.errors(doc, allowed))
        doc["spec"]["group"] = "monitoring.coreos.com"
        self.assert_rejected(doc, "CRD group", allowed)

    def test_rbac_requires_review_and_cannot_bind_existing_namespace(self):
        doc = {"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRoleBinding",
               "metadata": {"name": "new-operator"}, "roleRef": {
                   "apiGroup": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": "new-operator"},
               "subjects": [{"kind": "ServiceAccount", "name": "operator", "namespace": "monitoring"}]}
        self.assert_rejected(doc, "allowlist")
        self.assert_rejected(doc, "subject", ["ClusterRoleBinding/new-operator"])

    def test_only_namespaced_role_binding_can_default_subject_namespace(self):
        doc = {"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "RoleBinding",
               "metadata": {"name": "redis", "namespace": "observability-alerting"},
               "roleRef": {"apiGroup": "rbac.authorization.k8s.io", "kind": "Role", "name": "redis"},
               "subjects": [{"kind": "ServiceAccount", "name": "redis"}]}
        self.assertEqual([], self.errors(doc))
        doc["subjects"][0]["namespace"] = "monitoring"
        self.assert_rejected(doc, "subject")
        doc["subjects"][0].pop("namespace")
        doc["kind"] = "ClusterRoleBinding"
        doc["metadata"].pop("namespace")
        doc["roleRef"]["kind"] = "ClusterRole"
        self.assert_rejected(doc, "subject", ["ClusterRoleBinding/redis"])

    def test_cluster_image_catalog_is_the_only_precise_cluster_crd_exception(self):
        doc = {"apiVersion": "apiextensions.k8s.io/v1", "kind": "CustomResourceDefinition",
               "metadata": {"name": "clusterimagecatalogs.postgresql.cnpg.io"}, "spec": {
                   "group": "postgresql.cnpg.io", "scope": "Cluster",
                   "names": {"plural": "clusterimagecatalogs", "kind": "ClusterImageCatalog"}}}
        allowed = ["CustomResourceDefinition/clusterimagecatalogs.postgresql.cnpg.io"]
        self.assert_rejected(doc, "allowlist")
        report = scope.validate_documents([doc], allowed)
        self.assertEqual([], report.errors)
        self.assertTrue(any("watch" in item and "webhook" in item for item in report.pending))
        for field, value in (("group", "operator.victoriametrics.com"), ("scope", "Namespaced"),
                             ("names", {"plural": "clusterimagecatalogs", "kind": "Cluster"})):
            bad = copy.deepcopy(doc)
            bad["spec"][field] = value
            self.assert_rejected(bad, "CRD group", allowed)
        doc["metadata"]["name"] = "clusters.postgresql.cnpg.io"
        doc["spec"]["names"] = {"plural": "clusters", "kind": "Cluster"}
        self.assert_rejected(doc, "CRD group", ["CustomResourceDefinition/clusters.postgresql.cnpg.io"])

    def test_isolated_cnpg_webhook_only(self):
        doc = webhook()
        allowed = ["ValidatingWebhookConfiguration/n9e-cnpg"]
        self.assertEqual([], self.errors(doc, allowed))
        for mutation in ("namespace", "group", "scope", "service"):
            bad = copy.deepcopy(doc)
            hook = bad["webhooks"][0]
            if mutation == "namespace":
                hook["namespaceSelector"] = {}
            elif mutation == "group":
                hook["rules"][0]["apiGroups"] = ["*"]
            elif mutation == "scope":
                hook["rules"][0]["scope"] = "*"
            else:
                hook["clientConfig"] = {"url": "https://outside.invalid"}
            self.assertTrue(self.errors(bad, allowed))

    def test_unknown_cr_is_not_claimed_as_complete_validation(self):
        doc = {"apiVersion": "example.invalid/v1", "kind": "FutureWorkload", "metadata": {
            "name": "future", "namespace": "observability-otel"}, "spec": {}}
        report = scope.validate_documents([doc])
        self.assertTrue(any("live" in item for item in report.pending))

    def test_vm_cluster_components_and_cnpg_storage_are_checked(self):
        component = {"priorityClassName": scope.PRIORITY_CLASS, "resources": resources(), "replicaCount": 1}
        doc = {"apiVersion": "operator.victoriametrics.com/v1beta1", "kind": "VMCluster",
               "metadata": {"name": "canary", "namespace": "observability-metrics"},
               "spec": {part: copy.deepcopy(component) for part in ("vmselect", "vminsert", "vmstorage")}}
        doc["spec"]["vmstorage"]["storage"] = {"volumeClaimTemplate": claim("10Gi")}
        self.assert_rejected(doc, "20Gi")
        doc["spec"]["vmstorage"]["storage"]["volumeClaimTemplate"] = claim()
        self.assertEqual([], self.errors(doc))
        self.assertTrue(scope.validate_documents([doc]).pending)

    def test_cnpg_cluster_and_clickhouse_embedded_templates_are_checked(self):
        doc = {"apiVersion": "postgresql.cnpg.io/v1", "kind": "Cluster", "metadata": {
            "name": "postgres", "namespace": "observability-alerting"}, "spec": {
                "priorityClassName": scope.PRIORITY_CLASS, "resources": resources(),
                "instances": 3, "storage": {"storageClass": scope.STORAGE_CLASS, "size": "20Gi"},
                "affinity": {"enablePodAntiAffinity": True, "podAntiAffinityType": "required",
                             "topologyKey": "kubernetes.io/hostname"}}}
        self.assertEqual([], self.errors(doc))
        doc["spec"]["storage"]["size"] = "10Gi"
        self.assert_rejected(doc, "20Gi")
        doc = {"apiVersion": "clickhouse.altinity.com/v1", "kind": "ClickHouseInstallation",
               "metadata": {"name": "logs", "namespace": "observability-analytics"}, "spec": {
                   "templates": {"podTemplates": [{"name": "server", "spec": pod_spec()}],
                                 "serviceTemplates": [{"name": "external", "spec": {"type": "LoadBalancer"}}]}}}
        self.assert_rejected(doc, "ClusterIP")
        doc["spec"]["templates"]["podTemplates"][0]["spec"]["hostNetwork"] = True
        self.assert_rejected(doc, "hostNetwork")

    def test_hard_spread_needs_enough_domains_for_persistent_replicas(self):
        doc = workload("StatefulSet", 3)
        doc["spec"]["volumeClaimTemplates"] = [claim()]
        spread = {"topologyKey": "kubernetes.io/hostname", "maxSkew": 1,
                  "whenUnsatisfiable": "DoNotSchedule", "labelSelector": {"matchLabels": {"app": "canary"}}}
        doc["spec"]["template"]["spec"]["topologySpreadConstraints"] = [spread]
        self.assert_rejected(doc, "anti-affinity")
        spread["minDomains"] = 3
        self.assertEqual([], self.errors(doc))

    def test_yaml_parser_failure_never_echoes_input_content(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "candidate.yaml"
            path.write_text("kind: Secret\nstringData: [DO-NOT-ECHO\n", encoding="utf-8")
            output = io.StringIO()
            with redirect_stderr(output):
                self.assertEqual(2, scope.main([str(path)]))
            self.assertNotIn("DO-NOT-ECHO", output.getvalue())

    def test_duplicate_yaml_keys_fail_closed(self):
        with self.assertRaises(scope.yaml.YAMLError):
            scope.yaml.load("kind: Deployment\nkind: Secret\n", Loader=scope.UniqueKeyLoader)


if __name__ == "__main__":
    unittest.main()
