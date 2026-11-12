import copy
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import Mock, patch

import promote
import publish
from release import CHART, IMAGE, Release, ReleaseError, update_desired_state

SHA = "a" * 40
DIGEST = "sha256:" + "b" * 64
CHART_DIGEST = "sha256:" + "c" * 64


def candidate(channel="production", version="0.1.0", run=10, attempt=1, sha=SHA):
    assert channel == "production"
    return Release.parse("refs/tags/VocabBank/v" + version, sha, run, attempt)


def initial():
    return {"defaultNamespace": "vocabbank", "deleteNamespace": False, "keepResources": True, "helm": {
        "repo": "oci://" + CHART, "version": "UNCONFIGURED", "releaseName": "vocabbank", "values": {
            "hostname": "vocab.vvivid.dev", "image": {"repository": IMAGE, "digest": "UNCONFIGURED"},
            "provenance": {"sourceSha": "UNCONFIGURED"}, "ingress": {"enabled": False},
            "tls": {"productionEnabled": False}, "maintenance": True, "persistence": {"claimName": "vocabbank-data"}, "resources": {"requests": {"cpu": "50m"}}}}}


class ReleasePolicyTests(unittest.TestCase):
    def test_production_only_identity(self):
        release = candidate()
        self.assertEqual(release.chart_version, "0.1.0")
        self.assertEqual(release.image_tag, "0.1.0")
        self.assertEqual(release.path.as_posix(), "environments/rd/vocabbank/production/fleet.yaml")
        for ref in ("refs/heads/main", "refs/heads/release/VocabBank/0.1.0", "refs/tags/v0.1.0",
                    "refs/tags/VocabBank/v01.1.0", "refs/tags/VocabBank/v0.1.0-rc.1",
                    "refs/tags/VocabBank/v0.1.0+build", "refs/tags/VocabBank/v0.1.0\n",
                    "refs/tags/vvivid.dev/v0.1.0", "refs/tags/VocabBank/v0.1.0/extra"):
            with self.subTest(ref=ref), self.assertRaises(ReleaseError):
                Release.parse(ref, SHA, 1, 1)

    def test_invalid_sha_run_and_digest(self):
        for sha, run, attempt in (("A" * 40, 1, 1), (SHA, 0, 1), (SHA, 1, "1;id")):
            with self.assertRaises(ReleaseError):
                Release.parse(candidate().ref, sha, run, attempt)
        for digest in ("latest", "sha256:" + "A" * 64, DIGEST + "\n"):
            with self.assertRaises(ReleaseError):
                candidate().provenance(digest, CHART_DIGEST)

    def test_updates_only_release_fields_and_preserves_operator_settings(self):
        document = initial()
        document["helm"]["values"]["resources"]["requests"]["cpu"] = "100m"
        document["helm"]["values"]["tls"]["productionEnabled"] = True
        before = copy.deepcopy(document)
        self.assertTrue(update_desired_state(document, candidate(), DIGEST, CHART_DIGEST))
        expected = copy.deepcopy(before)
        expected["helm"]["version"] = "0.1.0"
        expected["helm"]["values"]["image"]["digest"] = DIGEST
        expected["helm"]["values"]["provenance"] = candidate().provenance(DIGEST, CHART_DIGEST)
        self.assertEqual(document, expected)
        self.assertFalse(update_desired_state(document, candidate(), DIGEST, CHART_DIGEST))

    def test_reuse_stale_downgrade_and_substitution_fail_without_mutation(self):
        document = initial()
        update_desired_state(document, candidate(), DIGEST, CHART_DIGEST)
        for release, image, chart in (
                (candidate(run=11), DIGEST, CHART_DIGEST),
                (candidate(sha="d" * 40), DIGEST, CHART_DIGEST),
                (candidate(version="0.0.9", run=99), DIGEST, CHART_DIGEST),
                (candidate(), "sha256:" + "d" * 64, CHART_DIGEST),
                (candidate(), DIGEST, "sha256:" + "d" * 64)):
            attempt = copy.deepcopy(document)
            with self.assertRaises(ReleaseError):
                update_desired_state(attempt, release, image, chart)
            self.assertEqual(attempt, document)
        self.assertTrue(update_desired_state(document, candidate(version="0.1.1"), DIGEST, CHART_DIGEST))

    def test_target_drift_and_inconsistent_release_fail(self):
        for field, value in (("namespace", "default"), ("hostname", "vvivid.dev"), ("repo", "oci://other.invalid/chart"),
                             ("releaseName", "rd-gateway"), ("retention", True), ("partial", "0.1.0")):
            document = initial()
            if field == "namespace": document["defaultNamespace"] = value
            elif field == "hostname": document["helm"]["values"]["hostname"] = value
            elif field == "retention": document["deleteNamespace"] = value
            elif field == "partial": document["helm"]["version"] = value
            else: document["helm"][field] = value
            before = copy.deepcopy(document)
            with self.subTest(field=field), self.assertRaises(ReleaseError):
                update_desired_state(document, candidate(), DIGEST, CHART_DIGEST)
            self.assertEqual(document, before)
        document = initial()
        update_desired_state(document, candidate(), DIGEST, CHART_DIGEST)
        document["helm"]["version"] = "9.9.9"
        with self.assertRaisesRegex(ReleaseError, "Inconsistent"):
            update_desired_state(document, candidate(version="0.1.1"), DIGEST, CHART_DIGEST)

    def test_registry_preflight_blocks_any_tag_reuse(self):
        release = candidate()
        for occupied in ((IMAGE, release.source_tag), (IMAGE, release.image_tag), (CHART, release.chart_version)):
            registry = Mock()
            registry.digest.side_effect = lambda repository, tag: DIGEST if (repository, tag) == occupied else None
            with self.subTest(occupied=occupied), self.assertRaises(ReleaseError):
                publish.require_unused(registry, release)

    def test_source_tag_peeling_and_moved_ref(self):
        release = candidate()
        result = Mock(stdout=f"{'d' * 40}\t{release.ref}\n{SHA}\t{release.ref}^{{}}\n")
        with patch.object(promote, "git", return_value=result):
            promote.verify_source(release, {})
        for result in ("", f"{'d' * 40}\t{release.ref}\n"):
            with patch.object(promote, "git", return_value=Mock(stdout=result)), self.assertRaises(ReleaseError):
                promote.verify_source(release, {})


class PromotionTransactionTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.remote = self.root / "remote.git"
        self.writer = self.root / "writer"
        self.checkout = self.root / "candidate"
        self.command(None, "init", "--bare", "--initial-branch=main", str(self.remote))
        self.command(None, "clone", str(self.remote), str(self.writer))
        path = self.writer / candidate().path
        path.parent.mkdir(parents=True)
        path.write_text(json.dumps(initial()))
        (self.writer / "unrelated.txt").write_text("initial\n")
        self.commit(self.writer, "initial")
        self.command(self.writer, "push", "origin", "main")
        self.command(None, "clone", str(self.remote), str(self.checkout))

    def command(self, directory, *arguments):
        return subprocess.run(["git", *arguments], cwd=directory, check=True, capture_output=True, text=True).stdout

    def commit(self, directory, message):
        self.command(directory, "add", ".")
        self.command(directory, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "-c",
                     "commit.gpgsign=false", "commit", "-m", message)

    def remote_document(self):
        return json.loads(self.command(None, "--git-dir", str(self.remote), "show", "main:" + candidate().path.as_posix()))

    def test_real_git_promotes_exactly_one_file(self):
        self.assertEqual(promote.promote_checkout(self.checkout, candidate(), DIGEST, CHART_DIGEST, lambda: None), "recorded")
        self.assertEqual(self.remote_document()["helm"]["values"]["provenance"]["sourceSha"], SHA)
        self.assertEqual(self.command(None, "--git-dir", str(self.remote), "diff", "--name-only", "main~1", "main").strip(),
                         candidate().path.as_posix())

    def test_concurrent_operator_update_is_preserved(self):
        def concurrent(attempt):
            if attempt == 0:
                path = self.writer / candidate().path
                document = json.loads(path.read_text())
                document["helm"]["values"]["ingress"]["enabled"] = True
                path.write_text(json.dumps(document))
                (self.writer / "unrelated.txt").write_text("owner work\n")
                self.commit(self.writer, "operator change")
                self.command(self.writer, "push", "origin", "main")
        promote.promote_checkout(self.checkout, candidate(), DIGEST, CHART_DIGEST, lambda: None, before_push=concurrent)
        self.assertTrue(self.remote_document()["helm"]["values"]["ingress"]["enabled"])
        self.assertEqual(self.command(None, "--git-dir", str(self.remote), "show", "main:unrelated.txt"), "owner work\n")

    def test_newer_release_blocks_stale_retry(self):
        def concurrent(attempt):
            if attempt == 0:
                path = self.writer / candidate().path
                document = json.loads(path.read_text())
                update_desired_state(document, candidate(version="0.1.1"), DIGEST, CHART_DIGEST)
                path.write_text(json.dumps(document))
                self.commit(self.writer, "newer release")
                self.command(self.writer, "push", "origin", "main")
        with self.assertRaisesRegex(ReleaseError, "reuse or downgrade"):
            promote.promote_checkout(self.checkout, candidate(), DIGEST, CHART_DIGEST, lambda: None, before_push=concurrent)
        self.assertEqual(self.remote_document()["helm"]["version"], "0.1.1")

    def test_moved_source_does_not_change_remote(self):
        verify = Mock(side_effect=[None, ReleaseError("Source ref moved")])
        with self.assertRaises(ReleaseError):
            promote.promote_checkout(self.checkout, candidate(), DIGEST, CHART_DIGEST, verify)
        self.assertEqual(self.remote_document()["helm"]["version"], "UNCONFIGURED")

    def test_auth_or_protection_failure_stops(self):
        hook = self.remote / "hooks/pre-receive"
        hook.write_text("#!/bin/sh\nexit 1\n")
        hook.chmod(0o700)
        with self.assertRaisesRegex(ReleaseError, "authentication, protection or transport"):
            promote.promote_checkout(self.checkout, candidate(), DIGEST, CHART_DIGEST, lambda: None)
        self.assertEqual(self.remote_document()["helm"]["version"], "UNCONFIGURED")


if __name__ == "__main__":
    unittest.main()
