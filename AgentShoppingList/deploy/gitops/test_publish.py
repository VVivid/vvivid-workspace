"""Exercise publication failure ordering without registry/network credentials."""

import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import Mock, patch

import publish
from release import CHART, IMAGE, ReleaseError
from test_release import CHART_DIGEST, DIGEST, SHA, candidate


class ArtifactPublicationTests(unittest.TestCase):
    def setUp(self):
        self.release = candidate("production")
        self.artifacts = {}
        self.events = []
        self.chart_substitution = False
        self.image_substitution = False
        self.registry = Mock()
        self.registry.digest.side_effect = lambda repository, tag: self.artifacts.get((repository, tag))
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.output = Path(self.temporary.name) / "release.json"

    def command(self, *arguments, include_stderr=False):
        self.events.append(arguments)
        if arguments[:3] == ("docker", "image", "inspect"):
            digest = "sha256:" + "d" * 64 if self.image_substitution else DIGEST
            return json.dumps([{"Os": "linux", "Architecture": "amd64", "Config": {"Labels": {
                "org.opencontainers.image.revision": SHA, "dev.vvivid.source-ref": self.release.ref,
                "dev.vvivid.base-url": self.release.base_url}}, "RepoDigests": [IMAGE + "@" + digest]}])
        if arguments[:2] == ("docker", "push"):
            repository, tag = arguments[2].rsplit(":", 1)
            self.artifacts[repository, tag] = DIGEST
        if arguments[:2] == ("helm", "push"):
            self.artifacts[CHART, self.release.chart_version] = (
                "sha256:" + "d" * 64 if self.chart_substitution else CHART_DIGEST)
            self.assertTrue(include_stderr)
            return "Pushed: test chart\nDigest: " + CHART_DIGEST + "\n"
        return ""

    def execute(self, verify=lambda: None):
        with patch.object(publish, "run", side_effect=self.command):
            publish.publish("local:checked", self.release, self.registry, verify, self.output)

    def test_records_exact_artifacts_after_complete_publication(self):
        self.execute()
        provenance = json.loads(self.output.read_text())
        self.assertEqual(provenance["imageDigest"], DIGEST)
        self.assertEqual(provenance["chartDigest"], CHART_DIGEST)
        self.assertEqual(self.artifacts[IMAGE, self.release.image_tag], DIGEST)
        self.assertLess(self.events.index(("docker", "push", IMAGE + ":" + self.release.source_tag)),
                        next(index for index, event in enumerate(self.events) if event[:2] == ("helm", "push")))

    def test_image_substitution_stops_before_chart_or_production_alias(self):
        self.image_substitution = True
        with self.assertRaisesRegex(ReleaseError, "image digest substitution"):
            self.execute()
        self.assertNotIn((CHART, self.release.chart_version), self.artifacts)
        self.assertNotIn((IMAGE, self.release.image_tag), self.artifacts)
        self.assertFalse(self.output.exists())

    def test_chart_substitution_stops_before_production_alias_or_metadata(self):
        self.chart_substitution = True
        with self.assertRaisesRegex(ReleaseError, "chart digest substitution"):
            self.execute()
        self.assertNotIn((IMAGE, self.release.image_tag), self.artifacts)
        self.assertFalse(self.output.exists())

    def test_existing_production_version_has_no_publication_side_effect(self):
        self.artifacts[IMAGE, self.release.image_tag] = DIGEST
        with self.assertRaisesRegex(ReleaseError, "Production version already exists"):
            self.execute()
        self.assertEqual(self.events, [])
        self.assertFalse(self.output.exists())

    def test_source_movement_before_upload_stops_all_pushes(self):
        verify = Mock(side_effect=[None, ReleaseError("Source ref moved")])
        with self.assertRaises(ReleaseError):
            self.execute(verify)
        self.assertFalse(self.artifacts)
        self.assertFalse(self.output.exists())

    def test_registry_auth_redirect_fails_closed(self):
        with self.assertRaisesRegex(ReleaseError, "redirected"):
            publish.NoRedirect().redirect_request(None, None, 302, None, None, "https://example.invalid")


if __name__ == "__main__":
    unittest.main()
