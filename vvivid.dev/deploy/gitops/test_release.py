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


def candidate(channel="staging", version="1.2.3", run=10, attempt=1, sha=SHA):
    prefix = "refs/heads/release/vvivid.dev/" if channel == "staging" else "refs/tags/vvivid.dev/v"
    return Release.parse(prefix + version, sha, run, attempt)


def initial(channel="staging"):
    return {"defaultNamespace": "website-" + channel, "helm": {
        "repo": "oci://" + CHART, "version": "UNCONFIGURED", "releaseName": "vvivid-website",
        "values": {"hostname": "stage.vvivid.dev" if channel == "staging" else "vvivid.dev",
                   "image": {"repository": IMAGE, "digest": "UNCONFIGURED"},
                   "provenance": {"sourceSha": "UNCONFIGURED"}, "ingress": {"enabled": False}}}}


class ReleasePolicyTests(unittest.TestCase):
    def test_release_identities_are_channel_specific(self):
        staging, production = candidate(), candidate("production")
        self.assertEqual(staging.image_tag, "release-1.2.3")
        self.assertEqual(staging.chart_version, "1.2.3-staging.10.1")
        self.assertEqual(staging.base_url, "https://stage.vvivid.dev/")
        self.assertIn("staging-1.2.3-sha-", staging.source_tag)
        self.assertEqual(production.image_tag, "1.2.3")
        self.assertEqual(production.chart_version, "1.2.3")
        self.assertEqual(production.base_url, "https://vvivid.dev/")
        self.assertNotEqual(staging.source_tag, production.source_tag)
        self.assertNotEqual(staging.source_tag, candidate(attempt=2).source_tag)

    def test_no_main_old_preview_or_noncanonical_versions(self):
        for ref in ("refs/heads/main", "refs/heads/release/vvivid-preview", "refs/tags/v1.0.0",
                    "refs/heads/release/vvivid.dev/01.2.3", "refs/tags/vvivid.dev/v1.2.3-rc.1",
                    "refs/heads/release/vvivid.dev/1.2.3/extra", "refs/heads/release/vvivid.dev/1.2.3\n"):
            with self.subTest(ref=ref), self.assertRaises(ReleaseError):
                Release.parse(ref, SHA, 1, 1)

    def test_invalid_sha_run_and_digest(self):
        for sha, run, attempt in (("A" * 40, 1, 1), (SHA, 0, 1), (SHA, 1, "1;id")):
            with self.assertRaises(ReleaseError):
                Release.parse(candidate().ref, sha, run, attempt)
        for digest in ("latest", "sha256:" + "A" * 64, DIGEST + "\n"):
            with self.assertRaises(ReleaseError):
                candidate().provenance(digest, CHART_DIGEST)

    def test_updates_only_owned_fields_and_is_idempotent(self):
        document = initial()
        self.assertTrue(update_desired_state(document, candidate(), DIGEST, CHART_DIGEST))
        self.assertFalse(update_desired_state(document, candidate(), DIGEST, CHART_DIGEST))
        self.assertEqual(document["helm"]["values"]["ingress"], {"enabled": False})
        self.assertEqual(document["helm"]["version"], candidate().chart_version)

    def test_stale_runs_attempts_and_older_versions_fail(self):
        document = initial()
        update_desired_state(document, candidate(run=20, attempt=2), DIGEST, CHART_DIGEST)
        for release in (candidate(run=19), candidate(run=20), candidate(version="1.2.2", run=99)):
            with self.subTest(release=release), self.assertRaises(ReleaseError):
                update_desired_state(copy.deepcopy(document), release, DIGEST, CHART_DIGEST)
        self.assertTrue(update_desired_state(document, candidate(run=20, attempt=3), DIGEST, CHART_DIGEST))

    def test_recorded_release_cannot_substitute_image_digest(self):
        document = initial()
        update_desired_state(document, candidate(), DIGEST, CHART_DIGEST)
        with self.assertRaisesRegex(ReleaseError, "substitution"):
            update_desired_state(document, candidate(), "sha256:" + "d" * 64, CHART_DIGEST)
        # Rebuilding a branch is a new staging artifact, with its own identity.
        self.assertTrue(update_desired_state(document, candidate(run=11), "sha256:" + "d" * 64, CHART_DIGEST))

    def test_production_reuse_and_downgrade_fail(self):
        document = initial("production")
        update_desired_state(document, candidate("production"), DIGEST, CHART_DIGEST)
        for release in (candidate("production", run=99), candidate("production", version="1.2.2", run=99)):
            with self.assertRaisesRegex(ReleaseError, "reuse or downgrade"):
                update_desired_state(copy.deepcopy(document), release, DIGEST, CHART_DIGEST)
        self.assertTrue(update_desired_state(document, candidate("production", version="1.2.4"), DIGEST, CHART_DIGEST))

    def test_mismatched_target_or_inconsistent_current_state_fails(self):
        document = initial()
        with self.assertRaises(ReleaseError):
            update_desired_state(document, candidate("production"), DIGEST, CHART_DIGEST)
        update_desired_state(document, candidate(), DIGEST, CHART_DIGEST)
        document["helm"]["version"] = "9.9.9"
        with self.assertRaisesRegex(ReleaseError, "Inconsistent"):
            update_desired_state(document, candidate(run=11), DIGEST, CHART_DIGEST)

    def test_wrong_hostname_is_rejected_without_changing_desired_state(self):
        for channel, hostname in (("staging", "preview.vvivid.dev"), ("staging", "vvivid.dev"),
                                  ("production", "stage.vvivid.dev")):
            document = initial(channel)
            document["helm"]["values"]["hostname"] = hostname
            before = copy.deepcopy(document)
            with self.subTest(channel=channel, hostname=hostname), self.assertRaisesRegex(ReleaseError, "hostname"):
                update_desired_state(document, candidate(channel), DIGEST, CHART_DIGEST)
            self.assertEqual(document, before)

    def test_registry_preflight_blocks_partial_reuse(self):
        release = candidate("production")
        for occupied in ((IMAGE, release.source_tag), (IMAGE, release.image_tag), (CHART, release.chart_version)):
            registry = Mock()
            registry.digest.side_effect = lambda repository, tag: DIGEST if (repository, tag) == occupied else None
            with self.subTest(occupied=occupied), self.assertRaises(ReleaseError):
                publish.require_unused(registry, release)

    def test_source_tag_peeling_and_moved_ref(self):
        release = candidate("production")
        result = Mock(stdout=f"{'d' * 40}\t{release.ref}\n{SHA}\t{release.ref}^{{}}\n")
        with patch.object(promote, "git", return_value=result):
            promote.verify_source(release, {})
        for text in ("", f"{'d' * 40}\t{release.ref}\n"):
            with patch.object(promote, "git", return_value=Mock(stdout=text)), self.assertRaises(ReleaseError):
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
        for channel in ("staging", "production"):
            path = self.writer / candidate(channel).path
            path.parent.mkdir(parents=True)
            path.write_text(json.dumps(initial(channel)))
        (self.writer / "unrelated.txt").write_text("initial\n")
        self.commit(self.writer, "initial")
        self.command(self.writer, "push", "origin", "main")
        self.command(None, "clone", str(self.remote), str(self.checkout))

    def command(self, directory, *arguments):
        return subprocess.run(["git", *arguments], cwd=directory, check=True,
                              capture_output=True, text=True).stdout

    def commit(self, directory, message):
        self.command(directory, "add", ".")
        self.command(directory, "-c", "user.name=Test", "-c", "user.email=test@example.invalid",
                     "-c", "commit.gpgsign=false", "commit", "-m", message)

    def remote_document(self):
        text = self.command(None, "--git-dir", str(self.remote), "show", "main:" + candidate().path.as_posix())
        return json.loads(text)

    def test_real_git_promotes_one_file(self):
        self.assertEqual(promote.promote_checkout(self.checkout, candidate(), DIGEST, CHART_DIGEST, lambda: None), "recorded")
        self.assertEqual(self.remote_document()["helm"]["values"]["provenance"]["sourceSha"], SHA)
        changed = self.command(None, "--git-dir", str(self.remote), "diff", "--name-only", "main~1", "main")
        self.assertEqual(changed.strip(), candidate().path.as_posix())

    def test_concurrent_unrelated_update_is_preserved(self):
        def concurrent(attempt):
            if attempt == 0:
                (self.writer / "unrelated.txt").write_text("concurrent owner work\n")
                self.commit(self.writer, "unrelated")
                self.command(self.writer, "push", "origin", "main")
        promote.promote_checkout(self.checkout, candidate(), DIGEST, CHART_DIGEST,
                                 lambda: None, before_push=concurrent)
        self.assertEqual(self.command(None, "--git-dir", str(self.remote), "show", "main:unrelated.txt"), "concurrent owner work\n")
        self.assertEqual(self.remote_document()["helm"]["version"], candidate().chart_version)

    def test_racing_newer_release_blocks_old_retry(self):
        def concurrent(attempt):
            if attempt == 0:
                path = self.writer / candidate().path
                document = json.loads(path.read_text())
                update_desired_state(document, candidate(run=20), DIGEST, CHART_DIGEST)
                path.write_text(json.dumps(document))
                self.commit(self.writer, "newer release")
                self.command(self.writer, "push", "origin", "main")
        with self.assertRaisesRegex(ReleaseError, "Superseded"):
            promote.promote_checkout(self.checkout, candidate(), DIGEST, CHART_DIGEST,
                                     lambda: None, before_push=concurrent)
        self.assertEqual(self.remote_document()["helm"]["values"]["provenance"]["runId"], 20)

    def test_source_moving_before_push_does_not_change_remote(self):
        verify = Mock(side_effect=[None, ReleaseError("Source ref moved")])
        with self.assertRaises(ReleaseError):
            promote.promote_checkout(self.checkout, candidate(), DIGEST, CHART_DIGEST, verify)
        self.assertEqual(self.remote_document()["helm"]["version"], "UNCONFIGURED")

    def test_protected_branch_or_auth_failure_stops(self):
        hook = self.remote / "hooks/pre-receive"
        hook.write_text("#!/bin/sh\nexit 1\n")
        hook.chmod(0o700)
        with self.assertRaisesRegex(ReleaseError, "authentication, protection or transport"):
            promote.promote_checkout(self.checkout, candidate(), DIGEST, CHART_DIGEST, lambda: None)
        self.assertEqual(self.remote_document()["helm"]["version"], "UNCONFIGURED")


if __name__ == "__main__":
    unittest.main()
