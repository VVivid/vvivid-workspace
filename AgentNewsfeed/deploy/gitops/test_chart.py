#!/usr/bin/env python3
"""Render nondeployable release fixtures and assert production trust/exposure invariants."""

import copy
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

import yaml

CHART = Path(__file__).resolve().parents[2] / "chart"
HELM = os.environ.get("HELM", "helm")
FIXTURE = {"image": {"digest": "sha256:" + "a" * 64}, "provenance": {"sourceSha": "b" * 40}}


def render(values):
    with tempfile.TemporaryDirectory(prefix="newsfeed-chart-") as directory:
        path = Path(directory) / "values.json"
        path.write_text(json.dumps(values))
        return subprocess.run([HELM, "template", "agent-newsfeed", str(CHART), "--namespace", "vvivid-rd",
                               "-f", str(path)], text=True, capture_output=True)


class ChartTests(unittest.TestCase):
    def resources(self, public=False):
        values = copy.deepcopy(FIXTURE)
        if public:
            values.update({"ingress": {"enabled": True}, "tls": {"productionEnabled": True}})
        result = render(values)
        self.assertEqual(result.returncode, 0, result.stderr)
        return list(yaml.safe_load_all(result.stdout))

    def test_strict_lint_and_schema_reject_unconfigured_or_wrong_trust(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "values.json"
            path.write_text(json.dumps(FIXTURE))
            subprocess.run([HELM, "lint", "--strict", str(CHART), "-f", str(path)], check=True)
        for values in ({}, {**FIXTURE, "hostname": "vvivid.dev"},
                       {**FIXTURE, "image": {"digest": "latest"}},
                       {**FIXTURE, "identity": {"gatewayAudience": "demo-a-mcp"}},
                       {**FIXTURE, "identity": {"mcpClientIDs": "rd-chatgpt-demo-a"}},
                       {**FIXTURE, "persistence": {"storageClassName": "local-path"}},
                       {**FIXTURE, "replicas": 2}, {**FIXTURE, "tunnel": {"enabled": True}},
                       {**FIXTURE, "ingress": {"enabled": True}}):
            with self.subTest(values=values):
                self.assertNotEqual(render(values).returncode, 0)

    def test_operator_maintenance_stops_writer_without_removing_claim(self):
        result = render({**FIXTURE, "maintenance": True, "persistence": {"claimName": "agent-newsfeed-data"}})
        self.assertEqual(result.returncode, 0, result.stderr)
        documents = list(yaml.safe_load_all(result.stdout))
        deployment = next(d for d in documents if d["kind"] == "Deployment")
        self.assertEqual(deployment["spec"]["replicas"], 0)
        self.assertTrue(any(d["kind"] == "PersistentVolumeClaim" and d["metadata"]["name"] == "agent-newsfeed-data" for d in documents))

    def test_only_new_app_resources_and_retained_pvc(self):
        documents = self.resources()
        self.assertEqual(sorted(d["kind"] for d in documents),
                         ["Certificate", "Deployment", "Ingress", "NetworkPolicy", "PersistentVolumeClaim", "Service"])
        pvc = next(d for d in documents if d["kind"] == "PersistentVolumeClaim")
        self.assertEqual(pvc["metadata"]["annotations"]["helm.sh/resource-policy"], "keep")
        self.assertEqual(pvc["spec"]["storageClassName"], "rd-local-retain")
        self.assertEqual(pvc["spec"]["resources"]["requests"]["storage"], "5Gi")
        service = next(d for d in documents if d["kind"] == "Service")
        self.assertEqual(service["spec"]["type"], "ClusterIP")
        self.assertFalse(any(d["kind"] in ("Namespace", "Issuer", "Secret", "ServiceAccount", "ResourceQuota") for d in documents))

    def test_nonroot_private_secret_copy_and_single_sqlite_writer(self):
        deployment = next(d for d in self.resources() if d["kind"] == "Deployment")
        self.assertEqual(deployment["spec"]["replicas"], 1)
        self.assertEqual(deployment["spec"]["strategy"], {"type": "Recreate"})
        pod = deployment["spec"]["template"]["spec"]
        self.assertFalse(pod["automountServiceAccountToken"])
        self.assertEqual(pod["securityContext"]["runAsUser"], 65532)
        self.assertTrue(pod["securityContext"]["runAsNonRoot"])
        init, = pod["initContainers"]
        self.assertIn("chmod 0600 /run/identity/web-client-secret /run/identity/session-keys.json", init["args"][0])
        self.assertIn("umask 077", init["args"][0])
        container, = pod["containers"]
        self.assertEqual(container["resources"], {"requests": {"cpu": "100m", "memory": "128Mi"}, "limits": {"cpu": "500m", "memory": "512Mi"}})
        self.assertTrue(container["securityContext"]["readOnlyRootFilesystem"])
        self.assertFalse(container["securityContext"]["allowPrivilegeEscalation"])
        self.assertEqual(container["securityContext"]["capabilities"]["drop"], ["ALL"])
        self.assertEqual([m["mountPath"] for m in container["volumeMounts"] if not m.get("readOnly")], ["/data"])
        self.assertNotIn("identity-seed", [m["name"] for m in container["volumeMounts"]])
        volumes = {v["name"]: v for v in pod["volumes"]}
        self.assertEqual(volumes["identity"]["emptyDir"]["medium"], "Memory")
        self.assertEqual(volumes["identity-seed"]["secret"]["defaultMode"], 0o440)
        self.assertEqual(volumes["gateway-jwks"]["configMap"]["name"], "rd-gateway-public-jwks")
        env = {e["name"]: e["value"] for e in container["env"]}
        self.assertEqual(env["NEWSFEED_MCP_AUTH_MODE"], "gateway")
        self.assertEqual(env["NEWSFEED_MCP_AUDIENCE"], "agent-newsfeed-mcp")
        self.assertEqual(env["NEWSFEED_API_AUDIENCE"], "https://newsfeed.vvivid.dev/api/v1")
        self.assertEqual(env["NEWSFEED_DB_PATH"], "/data/sqlite/newsfeed.sqlite")

    def test_private_mcp_exact_paths_and_public_browser_allowlist(self):
        documents = self.resources(public=True)
        ingresses = [d for d in documents if d["kind"] == "Ingress"]
        self.assertEqual(len(ingresses), 2)
        for ingress in ingresses:
            entrypoint = ingress["metadata"]["annotations"]["traefik.ingress.kubernetes.io/router.entrypoints"]
            rule, = ingress["spec"]["rules"]
            paths = rule["http"]["paths"]
            if entrypoint == "platform-tls":
                self.assertEqual(rule["host"], "mcp.rd.lab.vvivid.dev")
                self.assertEqual({p["path"] for p in paths}, {"/agent-newsfeed/mcp", "/.well-known/oauth-protected-resource/agent-newsfeed/mcp"})
                self.assertTrue(all(p["pathType"] == "Exact" and p["backend"]["service"]["name"] == "rd-gateway" for p in paths))
            else:
                self.assertEqual(entrypoint, "websecure")
                self.assertEqual(rule["host"], "newsfeed.vvivid.dev")
                allowed = {(p["path"], p["pathType"]) for p in paths}
                self.assertEqual(allowed, {(p, "Exact") for p in ("/", "/archive", "/auth/login", "/auth/callback", "/auth/logout")} |
                                 {(p, "Prefix") for p in ("/articles/", "/api/v1/articles", "/static/assets/", "/thumbnails/")})
                def matches(request):
                    return any(request == path if kind == "Exact" else request.rstrip("/") == path.rstrip("/") or
                               request.startswith(path.rstrip("/") + "/") for path, kind in allowed)
                for path in ("/", "/auth/callback", "/archive", "/api/v1/articles/abc/archive", "/articles/abc", "/static/assets/app.js", "/thumbnails/abc", "/thumbnails/abc/content/" + "a" * 64):
                    self.assertTrue(matches(path), path)
                for path in ("/mcp", "/mcp/", "/health/live", "/health/ready", "/.well-known/oauth-protected-resource/mcp", "/api/admin", "/api/v1/articles-evil", "/auth/admin"):
                    self.assertFalse(matches(path), path)
                self.assertTrue(all(p["backend"]["service"]["name"] == "agent-newsfeed" for p in paths))
        self.assertEqual(len([d for d in documents if d["kind"] == "Certificate"]), 2)

    def test_only_identified_traefik_and_gateway_can_enter(self):
        policy = next(d for d in self.resources() if d["kind"] == "NetworkPolicy")
        rules = policy["spec"]["ingress"]
        self.assertEqual(len(rules), 2)
        traefik, gateway = rules
        self.assertEqual(traefik["from"], [{"namespaceSelector": {"matchLabels": {"kubernetes.io/metadata.name": "kube-system"}},
                                         "podSelector": {"matchLabels": {"app.kubernetes.io/name": "traefik", "app.kubernetes.io/instance": "traefik-kube-system"}}}])
        self.assertEqual(gateway["from"], [{"podSelector": {"matchLabels": {"app.kubernetes.io/name": "rd-gateway"}}}])
        self.assertTrue(all(r["ports"] == [{"protocol": "TCP", "port": 8091}] for r in rules))


if __name__ == "__main__":
    unittest.main()
