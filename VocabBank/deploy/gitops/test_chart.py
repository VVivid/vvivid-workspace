"""Render the app-only chart and test its routing, storage and trust boundaries."""
import copy
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

import yaml

from test_release import CHART_DIGEST, DIGEST, candidate

CHART = Path(__file__).resolve().parents[2] / "chart"
HELM = os.environ.get("HELM", "helm")


def values():
    document = yaml.safe_load((CHART / "values.yaml").read_text())
    document["image"]["digest"] = DIGEST
    document["provenance"] = candidate().provenance(DIGEST, CHART_DIGEST)
    document["maintenance"] = False
    return document


def render(document):
    with tempfile.NamedTemporaryFile(mode="w", suffix=".json") as temporary:
        json.dump(document, temporary)
        temporary.flush()
        return subprocess.run([HELM, "template", "vocabbank", str(CHART), "--namespace", "vocabbank",
                               "--values", temporary.name], text=True, capture_output=True)


class ChartTests(unittest.TestCase):
    def resources(self, *, public=False, maintenance=False):
        document = values()
        document["maintenance"] = maintenance
        if public:
            document["ingress"]["enabled"] = True
            document["tls"] = {"productionEnabled": True, "trustedCertificateReady": True}
        result = render(document)
        self.assertEqual(result.returncode, 0, result.stderr)
        return [item for item in yaml.safe_load_all(result.stdout) if item]

    def test_placeholders_and_unrecognized_configuration_fail_closed(self):
        self.assertNotEqual(render(yaml.safe_load((CHART / "values.yaml").read_text())).returncode, 0)
        cases = [
            ("hostname", "other.vvivid.dev"), ("replicaCount", 2), ("replicaCount", 0),
            ("routes", {"public": ["/mcp"]}), ("identity.webClientID", "newsfeed-web"),
            ("identity.existingSecret", "rd-gateway-signing"),
            ("image.repository", "registry.lab.vvivid.dev/products/agent-newsfeed"),
            ("image.digest", "latest"), ("persistence.claimName", "other-data"),
            ("persistence.storageClassName", "local-path"), ("persistence.size", "10Gi"),
            ("resources.limits.memory", "2Gi"), ("resources.requests.cpu", "500m"),
            ("imagePullSecrets", [{"name": "rd-registry-pull"}]),
            ("initImage", "postgres:latest"), ("provenance.sourceSha", "UNCONFIGURED"),
            ("provenance.channel", "staging"), ("provenance.runId", 0),
            ("provenance.sourceRef", "refs/tags/VocabBank/v0.1.1"),
            ("provenance.runUrl", "https://github.com/VVivid/vvivid-workspace/actions/runs/11"),
            ("provenance.imageDigest", "sha256:" + "e" * 64),
            ("provenance.chartVersion", "0.1.1"), ("provenance.chartDigest", "UNCONFIGURED"),
        ]
        for field, value in cases:
            document = values()
            target = document
            parts = field.split(".")
            for part in parts[:-1]:
                target = target[part]
            target[parts[-1]] = value
            with self.subTest(field=field):
                self.assertNotEqual(render(document).returncode, 0)

    def test_public_ingress_requires_both_trusted_tls_gates(self):
        for production, ready in [(False, False), (True, False), (False, True)]:
            document = values()
            document["ingress"]["enabled"] = True
            document["tls"] = {"productionEnabled": production, "trustedCertificateReady": ready}
            self.assertNotEqual(render(document).returncode, 0)
        self.assertFalse(any(item["kind"] == "Ingress" for item in self.resources()))

    def test_app_only_ownership_and_single_writer(self):
        resources = self.resources()
        self.assertEqual({item["kind"] for item in resources}, {
            "ServiceAccount", "Deployment", "Service", "PersistentVolumeClaim", "NetworkPolicy"})
        deployment = next(item for item in resources if item["kind"] == "Deployment")
        self.assertEqual(deployment["spec"]["replicas"], 1)
        self.assertEqual(deployment["spec"]["strategy"], {"type": "Recreate"})
        stopped = next(item for item in self.resources(maintenance=True) if item["kind"] == "Deployment")
        self.assertEqual(stopped["spec"]["replicas"], 0)
        claim = next(item for item in resources if item["kind"] == "PersistentVolumeClaim")
        self.assertEqual(claim["metadata"]["name"], "vocabbank-acceptance")
        self.assertEqual(claim["metadata"]["annotations"]["helm.sh/resource-policy"], "keep")
        self.assertEqual(claim["spec"]["storageClassName"], "rd-local-retain")
        self.assertEqual(claim["spec"]["resources"]["requests"]["storage"], "5Gi")
        document = values()
        document["persistence"]["claimName"] = "vocabbank-data"
        self.assertEqual(render(document).returncode, 0)

    def test_runtime_private_files_and_environment_match_real_cli(self):
        deployment = next(item for item in self.resources() if item["kind"] == "Deployment")
        pod = deployment["spec"]["template"]["spec"]
        self.assertFalse(pod["automountServiceAccountToken"])
        self.assertEqual(pod["serviceAccountName"], "vocabbank")
        self.assertEqual(pod["securityContext"]["runAsUser"], 65532)
        self.assertEqual(pod["securityContext"]["runAsGroup"], 65532)
        self.assertTrue(pod["securityContext"]["runAsNonRoot"])
        self.assertEqual(pod["securityContext"]["seccompProfile"]["type"], "RuntimeDefault")
        self.assertEqual(pod["nodeSelector"], {"kubernetes.io/arch": "amd64"})
        init, = pod["initContainers"]
        self.assertIn("umask 077", init["args"][0])
        self.assertIn("chmod 0600 /run/identity/web-client-secret /run/identity/session-keys.json", init["args"][0])
        self.assertIn("chmod 0700 /data/sqlite", init["args"][0])
        container, = pod["containers"]
        for component in (init, container):
            self.assertEqual(component["securityContext"], {"allowPrivilegeEscalation": False,
                "readOnlyRootFilesystem": True, "capabilities": {"drop": ["ALL"]}})
            self.assertIn("ephemeral-storage", component["resources"]["requests"])
            self.assertIn("ephemeral-storage", component["resources"]["limits"])
        self.assertEqual(container["args"], ["-listen=:8090", "-db=/data/sqlite/vocabbank.sqlite", "-static=/static"])
        self.assertEqual([mount["mountPath"] for mount in container["volumeMounts"] if not mount.get("readOnly")], ["/data"])
        self.assertNotIn("identity-seed", [mount["name"] for mount in container["volumeMounts"]])
        volumes = {volume["name"]: volume for volume in pod["volumes"]}
        self.assertEqual(volumes["identity"]["emptyDir"], {"medium": "Memory", "sizeLimit": "1Mi"})
        self.assertEqual(volumes["identity-seed"]["secret"]["defaultMode"], 0o440)
        self.assertEqual(volumes["gateway-jwks"]["configMap"]["name"], "vocabbank-gateway-jwks")
        env = {entry["name"]: entry["value"] for entry in container["env"]}
        self.assertEqual(env, {
            "VOCAB_ORIGINS": "https://vocab.vvivid.dev", "VOCAB_OIDC_ISSUER": "https://auth.rd.vvivid.dev/realms/vvivid-rd",
            "VOCAB_WEB_CLIENT_ID": "vocabbank-web", "VOCAB_WEB_CLIENT_SECRET_FILE": "/run/identity/web-client-secret",
            "VOCAB_SESSION_KEYS_FILE": "/run/identity/session-keys.json", "VOCAB_API_AUDIENCE": "https://vocab.vvivid.dev/api/v1",
            "VOCAB_MCP_MODE": "gateway", "VOCAB_MCP_RESOURCE": "https://mcp.rd.lab.vvivid.dev/vocabbank/mcp",
            "VOCAB_MCP_AUDIENCE": "vocabbank-mcp", "VOCAB_GATEWAY_ISSUER": "https://mcp.rd.lab.vvivid.dev",
            "VOCAB_GATEWAY_JWKS_FILE": "/etc/gateway/public.jwks.json", "VOCAB_MCP_CLIENT_IDS": "vocabbank-chatgpt"})
        for probe in ("startupProbe", "readinessProbe", "livenessProbe"):
            self.assertEqual(container[probe]["httpGet"]["httpHeaders"], [{"name": "Host", "value": "vocab.vvivid.dev"}])

    def test_exact_public_browser_routes_exclude_private_surfaces(self):
        ingress, = [item for item in self.resources(public=True) if item["kind"] == "Ingress"]
        self.assertEqual(ingress["metadata"]["annotations"]["traefik.ingress.kubernetes.io/router.entrypoints"], "websecure")
        rule, = ingress["spec"]["rules"]
        self.assertEqual(rule["host"], "vocab.vvivid.dev")
        paths = {(item["path"], item["pathType"]) for item in rule["http"]["paths"]}
        self.assertEqual(paths, {(path, "Exact") for path in ["/", "/auth/login", "/auth/callback", "/auth/logout", "/api/v1/session"]}
                         | {(path, "Prefix") for path in ["/api/v1/words", "/_next/"]})
        def matches(request):
            return any(request == path or kind == "Prefix" and request.startswith(path.rstrip("/") + "/")
                       for path, kind in paths)
        for path in ["/", "/api/v1/words", "/api/v1/words/abc/completion", "/_next/static/app.js", "/auth/login", "/api/v1/session"]:
            self.assertTrue(matches(path), path)
        for path in ["/mcp", "/mcp/", "/health/live", "/health/ready", "/metrics", "/admin", "/.well-known/oauth-protected-resource/mcp",
                     "/.well-known/openid-configuration", "/api/v1/words-evil", "/api/v1/session/admin", "/auth/admin", "/_next-evil", "/index.html"]:
            self.assertFalse(matches(path), path)

    def test_namespace_and_pod_selectors_are_conjunctive(self):
        policy = next(item for item in self.resources() if item["kind"] == "NetworkPolicy")
        traefik, gateway = policy["spec"]["ingress"]
        self.assertEqual(traefik["from"], [{"namespaceSelector": {"matchLabels": {"kubernetes.io/metadata.name": "kube-system"}},
            "podSelector": {"matchLabels": {"app.kubernetes.io/name": "traefik", "app.kubernetes.io/instance": "traefik-kube-system"}}}])
        self.assertEqual(gateway["from"], [{"namespaceSelector": {"matchLabels": {"kubernetes.io/metadata.name": "vvivid-rd"}},
            "podSelector": {"matchLabels": {"app.kubernetes.io/name": "rd-gateway"}}}])
        self.assertTrue(all(rule["ports"] == [{"protocol": "TCP", "port": 8090}] for rule in [traefik, gateway]))


if __name__ == "__main__":
    unittest.main()
