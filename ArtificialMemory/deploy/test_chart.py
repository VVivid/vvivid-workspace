"""Local rendering/contract checks only. No Kubernetes API calls or deployment."""

import copy
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

import jsonschema
import yaml


HERE = Path(__file__).resolve().parent
CHART = HERE / "helm" / "artificial-memory"
EXAMPLE = HERE / "examples" / "values.yaml"
HELM = os.environ.get("HELM", "helm")


def merge(left, right):
    result = copy.deepcopy(left)
    for key, value in right.items():
        if isinstance(value, dict) and isinstance(result.get(key), dict):
            result[key] = merge(result[key], value)
        else:
            result[key] = copy.deepcopy(value)
    return result


class ChartTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.values = merge(yaml.safe_load((CHART / "values.yaml").read_text()), yaml.safe_load(EXAMPLE.read_text()))
        cls.schema = json.loads((CHART / "values.schema.json").read_text())
        jsonschema.Draft7Validator.check_schema(cls.schema)

    def render(self, values=None, extra=(), success=True):
        with tempfile.TemporaryDirectory(prefix="artificial-memory-chart-") as private:
            path = Path(private) / "values.yaml"
            path.write_text(yaml.safe_dump(values if values is not None else self.values))
            result = subprocess.run([HELM, "template", "artificial-memory", str(CHART), "--namespace", "memory", "-f", str(path), *extra], text=True, capture_output=True, check=False, timeout=30)
        if success:
            self.assertEqual(result.returncode, 0, result.stderr)
            return [doc for doc in yaml.safe_load_all(result.stdout) if doc]
        self.assertNotEqual(result.returncode, 0, "invalid chart values were accepted")
        return result

    def test_lint_and_private_single_writer_contract(self):
        jsonschema.validate(self.values, self.schema)
        result = subprocess.run([HELM, "lint", "--strict", str(CHART), "-f", str(EXAMPLE)], text=True, capture_output=True, check=False, timeout=30)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        docs = {doc["kind"]: doc for doc in self.render()}
        self.assertEqual(set(docs), {"Deployment", "Service", "PersistentVolumeClaim"})
        deployment = docs["Deployment"]["spec"]
        self.assertEqual(deployment["replicas"], 1)
        self.assertEqual(deployment["strategy"], {"type": "Recreate"})
        pod = deployment["template"]["spec"]
        self.assertFalse(pod["automountServiceAccountToken"])
        self.assertGreaterEqual(pod["terminationGracePeriodSeconds"], 15)
        self.assertEqual(pod["securityContext"], {"runAsNonRoot": True, "runAsUser": 65532, "runAsGroup": 65532, "fsGroup": 65532, "fsGroupChangePolicy": "OnRootMismatch", "seccompProfile": {"type": "RuntimeDefault"}})
        self.assertNotIn("initContainers", pod)
        self.assertNotIn("hostNetwork", pod)
        self.assertEqual(pod["nodeSelector"], self.values["nodeSelector"])
        self.assertEqual(len(pod["containers"]), 1)
        app = pod["containers"][0]
        self.assertEqual(app["securityContext"], {"allowPrivilegeEscalation": False, "readOnlyRootFilesystem": True, "capabilities": {"drop": ["ALL"]}})
        self.assertIn("@sha256:", app["image"])
        self.assertEqual(app["resources"], self.values["resources"])
        env = {item["name"]: item["value"] for item in app["env"]}
        self.assertEqual(env["MEMORY_DATABASE"], "/data/sqlite/memory.db")
        self.assertEqual(env["TMPDIR"], "/data/sqlite")
        self.assertEqual(env["SQLITE_TMPDIR"], "/data/sqlite")
        self.assertEqual(env["MEMORY_LISTEN"], ":8080")
        self.assertEqual(env["MEMORY_ALLOWED_CLIENT_IDS"], "coding-agent")
        mounts = {item["name"]: item for item in app["volumeMounts"]}
        self.assertEqual(mounts["journal"]["mountPath"], "/data")
        self.assertTrue(mounts["gateway-public"]["readOnly"])
        self.assertEqual(set(mounts), {"journal", "gateway-public"})
        volumes = {item["name"]: item for item in pod["volumes"]}
        public = volumes["gateway-public"]["configMap"]
        self.assertEqual(public["name"], self.values["trust"]["jwksConfigMap"])
        self.assertEqual(public["defaultMode"], 0o444)
        self.assertEqual(public["items"][0]["path"], "gateway-public.jwks")
        for probe, kind in [("startupProbe", "ready"), ("readinessProbe", "ready"), ("livenessProbe", "live")]:
            self.assertEqual(app[probe]["exec"]["command"], ["/artificial-memory", "healthcheck", kind])
            self.assertGreaterEqual(app[probe]["timeoutSeconds"], 3)
        service = docs["Service"]["spec"]
        self.assertEqual(service["type"], "ClusterIP")
        self.assertEqual(service["ports"][0]["targetPort"], "http")
        self.assertEqual(service["selector"], deployment["selector"]["matchLabels"])
        self.assertNotIn("externalIPs", service)
        pvc = docs["PersistentVolumeClaim"]
        self.assertEqual(pvc["metadata"]["annotations"]["helm.sh/resource-policy"], "keep")
        self.assertEqual(pvc["spec"]["accessModes"], ["ReadWriteOnce"])
        self.assertEqual(pvc["spec"]["storageClassName"], self.values["persistence"]["storageClass"])

    def test_maintenance_existing_claim_and_custom_port(self):
        values = copy.deepcopy(self.values)
        values["replicaCount"] = 0
        values["persistence"]["existingClaim"] = "retained-journal"
        values["service"]["port"] = 8123
        values["trust"]["allowedClientIDs"] = ["agent-one", "agent-two"]
        docs = {doc["kind"]: doc for doc in self.render(values)}
        self.assertEqual(set(docs), {"Deployment", "Service"})
        self.assertEqual(docs["Deployment"]["spec"]["replicas"], 0)
        pod = docs["Deployment"]["spec"]["template"]["spec"]
        self.assertEqual(pod["volumes"][0]["persistentVolumeClaim"]["claimName"], "retained-journal")
        env = {item["name"]: item["value"] for item in pod["containers"][0]["env"]}
        self.assertEqual(env["MEMORY_LISTEN"], ":8123")
        self.assertEqual(env["MEMORY_ALLOWED_CLIENT_IDS"], "agent-one,agent-two")
        self.assertEqual(docs["Service"]["spec"]["ports"][0]["port"], 8123)

    def test_invalid_values_fail_real_helm_render(self):
        bad = [
            {"replicaCount": 2}, {"replicaCount": -1}, {"replicaCount": 1.5},
            {"service": {"port": 0}}, {"service": {"type": "LoadBalancer"}},
            {"image": {"digest": "latest"}}, {"image": {"repository": ""}},
            {"trust": {"jwksConfigMap": ""}}, {"trust": {"allowedClientIDs": []}},
            {"trust": {"allowedClientIDs": ["a", "a"]}}, {"trust": {"allowedClientIDs": ["a,b"]}},
            {"trust": {"audience": "other-service"}}, {"trust": {"gatewayIssuer": "http://public.example"}},
            {"trust": {"backendOrigin": "http://host/mcp"}}, {"trust": {"identityIssuer": ""}},
            {"persistence": {"storageClass": ""}}, {"nodeSelector": {}},
            {"resources": {"limits": {"memory": "0Mi"}}},
            {"ingress": {"enabled": True}}, {"securityContext": {"runAsUser": 0}},
        ]
        for patch in bad:
            with self.subTest(patch=patch):
                values = merge(self.values, patch)
                if patch == {"nodeSelector": {}}:
                    values["nodeSelector"] = {}
                with self.assertRaises(jsonschema.ValidationError):
                    jsonschema.validate(values, self.schema)
                self.render(values, success=False)
        values = copy.deepcopy(self.values)
        values["replicaCount"] = 2
        self.render(values, extra=("--skip-schema-validation",), success=False)

    def test_illustrative_gateway_route_matches_chart(self):
        route = json.loads((HERE / "examples" / "gateway-route.json").read_text())
        trust = self.values["trust"]
        self.assertEqual(route["upstream"], trust["backendOrigin"] + "/mcp")
        self.assertEqual(route["upstream_host"], trust["backendOrigin"].split("://", 1)[1])
        self.assertEqual(route["audience"], trust["audience"])
        self.assertEqual(route["client_ids"], trust["allowedClientIDs"])
        self.assertEqual(route["required_scopes"], ["memory:read"])
        self.assertEqual(route["forward_scopes"], ["memory:read", "memory:write"])


if __name__ == "__main__":
    unittest.main()
