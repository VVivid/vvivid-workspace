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
# Public browser ingress (path, pathType) pairs while native app clients are disabled.
PUBLIC_BROWSER_ROUTES = (
    {(path, "Exact") for path in ("/", "/archive", "/trash", "/todo", "/settings", "/api/v1/tags", "/api/v1/todos",
                                  "/api/v1/settings/preferences", "/api/v1/settings/suggestions/accept",
                                  "/api/v1/settings/suggestions/dismiss", "/auth/login", "/auth/callback", "/auth/logout")} |
    {(path, "Prefix") for path in ("/articles/", "/api/v1/articles", "/static/assets/", "/thumbnails/")})
NATIVE_APP_DISCOVERY = "/.well-known/oauth-protected-resource/api/v1"


def render(values):
    with tempfile.TemporaryDirectory(prefix="newsfeed-chart-") as directory:
        path = Path(directory) / "values.json"
        path.write_text(json.dumps(values))
        return subprocess.run([HELM, "template", "agent-newsfeed", str(CHART), "--namespace", "vvivid-rd",
                               "-f", str(path)], text=True, capture_output=True)


def routed(allowed, request):
    """Match (path, pathType) pairs like Ingress: Exact whole paths, Prefix by path segment."""
    for path, kind in allowed:
        if request == path:
            return True
        if kind == "Prefix":
            descendant_prefix = path if path.endswith("/") else path + "/"
            if request.startswith(descendant_prefix):
                return True
    return False


class ChartTests(unittest.TestCase):
    def resources(self, public=False, native_clients=None):
        values = copy.deepcopy(FIXTURE)
        if public:
            values.update({"ingress": {"enabled": True}, "tls": {"productionEnabled": True}})
        if native_clients is not None:
            values["identity"] = {"nativeClientIDs": native_clients}
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
        # Explicitly empty: native app bearer access stays disabled by default.
        self.assertEqual(env["NEWSFEED_NATIVE_CLIENT_IDS"], "")

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
                self.assertEqual(allowed, PUBLIC_BROWSER_ROUTES)
                for path in ("/", "/auth/callback", "/archive", "/trash", "/api/v1/articles/abc/archive", "/articles/abc", "/api/v1/tags",
                             "/todo", "/settings", "/api/v1/todos", "/api/v1/settings/preferences", "/api/v1/settings/suggestions/accept", "/api/v1/settings/suggestions/dismiss", "/static/assets/app.js", "/thumbnails/abc", "/thumbnails/abc/content/" + "a" * 64):
                    self.assertTrue(routed(allowed, path), path)
                for path in ("/mcp", "/mcp/", "/health/live", "/health/ready", "/.well-known/oauth-protected-resource/mcp",
                             NATIVE_APP_DISCOVERY, "/api/admin", "/api/v1/articles-evil", "/api/v1/tags/", "/api/v1/tags/go", "/api/v1/tags-evil", "/auth/admin",
                             "/jobs", "/jobs/", "/jobs/archive", "/jobs/trash", "/jobs/abc", "/jobs-evil", "/jobsearch",
                             "/api/v1/opportunities", "/api/v1/opportunities/abc", "/api/v1/opportunities/abc/archive",
                             "/api/v1/opportunities/abc/unarchive", "/api/v1/opportunities/abc/trash", "/api/v1/opportunities/abc/restore",
                             "/api/v1/opportunities-evil", "/api/v1/opportunity",
                             "/settings/", "/settings-evil", "/api/v1/settings/admin", "/api/v1/settings/preferences-evil", "/todo/", "/todo/history", "/todo-evil", "/api/v1/todos/anything", "/api/v1/todos-evil"):
                    self.assertFalse(routed(allowed, path), path)
                self.assertTrue(all(p["backend"]["service"]["name"] == "agent-newsfeed" for p in paths))
        self.assertEqual(len([d for d in documents if d["kind"] == "Certificate"]), 2)

    def test_native_app_clients_change_only_their_env_and_exact_discovery_route(self):
        def by_kind_and_name(documents):
            return {(d["kind"], d["metadata"]["name"]): d for d in documents}
        disabled = by_kind_and_name(self.resources(public=True))
        enabled = by_kind_and_name(self.resources(public=True, native_clients="newsfeed-android"))
        changed = {key for key in disabled.keys() | enabled.keys() if disabled.get(key) != enabled.get(key)}
        self.assertEqual(changed, {("Deployment", "agent-newsfeed"), ("Ingress", "agent-newsfeed-browser")})
        container, = enabled["Deployment", "agent-newsfeed"]["spec"]["template"]["spec"]["containers"]
        self.assertIn({"name": "NEWSFEED_NATIVE_CLIENT_IDS", "value": "newsfeed-android"}, container["env"])
        rule, = enabled["Ingress", "agent-newsfeed-browser"]["spec"]["rules"]
        paths = rule["http"]["paths"]
        self.assertTrue(all(p["backend"]["service"] == {"name": "agent-newsfeed", "port": {"number": 8091}} for p in paths))
        allowed = {(p["path"], p["pathType"]) for p in paths}
        self.assertEqual(allowed, PUBLIC_BROWSER_ROUTES | {(NATIVE_APP_DISCOVERY, "Exact")})
        # Native article reads and state changes, and thumbnails, use the existing prefixes;
        # tag counts use the exact tag route that browser sessions share.
        for path in (NATIVE_APP_DISCOVERY, "/api/v1/articles", "/api/v1/articles/abc", "/api/v1/articles/abc/archive",
                     "/api/v1/articles/abc/restore", "/api/v1/articles/abc/trash", "/api/v1/articles/abc/restore-trash",
                     "/api/v1/tags", "/thumbnails/abc", "/thumbnails/abc/content/" + "a" * 64):
            self.assertTrue(routed(allowed, path), path)
        for path in (NATIVE_APP_DISCOVERY + "/", NATIVE_APP_DISCOVERY + "/extra", "/.well-known/oauth-protected-resource",
                     "/.well-known/oauth-protected-resource/mcp", "/.well-known/openid-configuration", "/mcp", "/health/ready"):
            self.assertFalse(routed(allowed, path), path)

    def test_native_app_client_ids_follow_the_app_configuration_rules(self):
        clients = [f"newsfeed-native-{i}" for i in range(17)]
        for accepted in ("newsfeed-android", "newsfeed-android,newsfeed-ios", ",".join(clients[:16]), "n" * 256):
            with self.subTest(accepted=accepted):
                deployment = next(d for d in self.resources(native_clients=accepted) if d["kind"] == "Deployment")
                container, = deployment["spec"]["template"]["spec"]["containers"]
                self.assertIn({"name": "NEWSFEED_NATIVE_CLIENT_IDS", "value": accepted}, container["env"])
        # The app refuses to start with any of these, so rendering must fail before the single pod
        # is replaced. The newsfeed.nativeClientIDs helper rejects well-formed lists that reuse an
        # ID; the schema rejects malformed ones, including a value that is not even a string.
        reused = ("newsfeed-web", "newsfeed-android,newsfeed-chatgpt", "newsfeed-android,newsfeed-android")
        malformed = ("newsfeed-android,", ",newsfeed-android", "newsfeed-android, newsfeed-ios", " newsfeed-android",
                     "newsfeed-android\n", ",".join(clients), "n" * 257, ["newsfeed-android"])
        for rejected in reused + malformed:
            with self.subTest(rejected=rejected):
                result = render({**FIXTURE, "identity": {"nativeClientIDs": rejected}})
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("nativeClientIDs", result.stderr)

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
