"""Deployment contract tests. All Docker/HTTP commands are replaced by fakes."""

from contextlib import ExitStack
import importlib.util
import json
import multiprocessing
import os
from pathlib import Path
import tempfile
import time
import unittest
from unittest.mock import Mock, patch

import deploy


DIGEST = "sha256:" + "a" * 64
CANDIDATE = deploy.IMAGE_REPOSITORY + "@" + DIGEST
PREVIOUS = "legacy.example/site:1.0.6"
PREVIOUS_ID = "sha256:" + "b" * 64
CANDIDATE_ID = "sha256:" + "c" * 64
LOCAL_URL = "http://traefik.invalid/"
INITIAL_ENV = "VVIVID_IMAGE=legacy.example/site\nVVIVID_LIVE_TAG=1.0.6\nPREVIEW_FLAG=keep\n"


class FakeCommands:
    def __init__(self, env_file):
        self.env_file = env_file
        self.current = PREVIOUS
        self.image_id = PREVIOUS_ID
        self.events = []
        self.fail_pull = False
        self.fail_up = False
        self.fail_health = False
        self.wrong_image = False
        self.supports_override = True
        self.change_preview = False

    def desired_image(self, override):
        if override is not None and self.supports_override:
            return override
        for line in self.env_file.read_text().splitlines():
            if line.startswith("VVIVID_LIVE_IMAGE="):
                return line.split("=", 1)[1]
        return PREVIOUS

    def __call__(self, *args, image=None, timeout=90):
        self.events.append((args, image))
        if args[:2] == ("docker", "compose"):
            expected = ("--project-name", "private-edge", "--env-file", str(self.env_file),
                        "--file", str(deploy.COMPOSE_FILE))
            assert args[2:8] == expected
            command = args[8:]
            if command == ("config", "--format", "json"):
                return json.dumps({"services": {"vvivid-live": {"image": self.desired_image(image)}}})
            if command == ("ps", "--quiet", "--status", "running", "vvivid-live"):
                return "live-container"
            assert command == ("up", "--detach", "--no-deps", "--pull", "never", "vvivid-live")
            self.current = self.desired_image(image)
            self.image_id = CANDIDATE_ID if self.current == CANDIDATE else PREVIOUS_ID
            if self.fail_up:
                self.fail_up = False
                raise deploy.DeploymentError("Fake partial Compose failure")
            return ""
        if args[:2] == ("docker", "pull"):
            assert args[2] == CANDIDATE
            assert timeout == 300
            if self.fail_pull:
                raise deploy.DeploymentError("Fake registry denied")
            return ""
        if args[:3] == ("docker", "image", "inspect"):
            return CANDIDATE_ID
        if args[:2] == ("docker", "inspect"):
            current = PREVIOUS if self.wrong_image and self.current == CANDIDATE else self.current
            return f"{current}\n{self.image_id}\ntrue"
        assert args[0] == "curl", args
        assert "Host: vvivid.dev" in args
        assert "Cache-Control: no-cache" in args
        assert "--location" not in args
        if self.current == CANDIDATE and self.fail_health:
            if self.change_preview:
                content = self.env_file.read_text().replace("PREVIEW_FLAG=keep", "PREVIEW_FLAG=updated")
                self.env_file.write_text(content)
            return "503"
        return "200"


class DeploymentTests(unittest.TestCase):
    def setUp(self):
        self.stack = ExitStack()
        self.addCleanup(self.stack.close)
        directory = Path(self.stack.enter_context(tempfile.TemporaryDirectory()))
        self.env_file = directory / ".env"
        self.env_file.write_text(INITIAL_ENV)
        self.env_file.chmod(0o664)
        self.fake = FakeCommands(self.env_file)
        self.stack.enter_context(patch.object(deploy, "ENV_FILE", self.env_file))
        self.stack.enter_context(patch.object(deploy, "LOCK_FILE", directory / "lock"))
        self.stack.enter_context(patch.object(deploy, "local_health_url", return_value=LOCAL_URL))
        self.stack.enter_context(patch.object(deploy, "run", side_effect=self.fake))
        self.stack.enter_context(patch.object(deploy.time, "sleep"))
        self.stack.enter_context(patch.object(deploy.signal, "signal"))
        writer = deploy.write_image_setting

        def record_write(setting):
            self.fake.events.append((("write", setting), None))
            writer(setting)

        self.stack.enter_context(patch.object(deploy, "write_image_setting", side_effect=record_write))

    def test_success_pulls_before_persistence_and_updates_only_live(self):
        deploy.deploy(DIGEST)
        commands = [args for args, _ in self.fake.events]
        pull = next(index for index, args in enumerate(commands) if args[:2] == ("docker", "pull"))
        write = next(index for index, args in enumerate(commands) if args[0] == "write")
        self.assertLess(pull, write)
        self.assertEqual(self.env_file.read_text(), INITIAL_ENV + f"VVIVID_LIVE_IMAGE={CANDIDATE}\n")
        self.assertEqual(self.env_file.stat().st_mode & 0o777, 0o664)
        self.assertEqual(self.fake.current, CANDIDATE)
        urls = [args[-1] for args in commands if args[0] == "curl"]
        self.assertTrue(urls[0].startswith(LOCAL_URL))
        self.assertTrue(urls[1].startswith("https://vvivid.dev/"))

    def test_invalid_or_foreign_image_cannot_run_any_command(self):
        for value in ["latest", "SHA256:" + "a" * 64, DIGEST + "; id", "other.example/site@" + DIGEST]:
            with self.subTest(value=value), self.assertRaises(deploy.DeploymentError):
                deploy.deploy(value)
        self.assertEqual(self.fake.events, [])

    def test_failed_pull_preserves_container_and_persisted_settings(self):
        self.fake.fail_pull = True
        with self.assertRaisesRegex(deploy.DeploymentError, "registry"):
            deploy.deploy(DIGEST)
        self.assertEqual(self.env_file.read_text(), INITIAL_ENV)
        self.assertEqual(self.fake.current, PREVIOUS)
        self.assertFalse(any(args[0] == "write" for args, _ in self.fake.events))

    def test_missing_compose_patch_fails_before_pulling_or_writing(self):
        self.fake.supports_override = False
        with self.assertRaisesRegex(deploy.DeploymentError, "separate live-image"):
            deploy.deploy(DIGEST)
        self.assertFalse(any(args[:2] == ("docker", "pull") for args, _ in self.fake.events))

    def test_partially_failed_compose_restores_previous_image_and_absent_key(self):
        self.fake.fail_up = True
        with self.assertRaisesRegex(deploy.DeploymentError, "previous live image restored"):
            deploy.deploy(DIGEST)
        self.assertEqual(self.fake.current, PREVIOUS)
        self.assertEqual(self.fake.image_id, PREVIOUS_ID)
        self.assertEqual(self.env_file.read_text(), INITIAL_ENV)

    def test_failed_health_restores_previous_digest_without_overwriting_preview_update(self):
        previous_setting = f"VVIVID_LIVE_IMAGE={PREVIOUS}\n"
        self.env_file.write_text(INITIAL_ENV + previous_setting)
        self.fake.fail_health = True
        self.fake.change_preview = True
        with self.assertRaisesRegex(deploy.DeploymentError, "previous live image restored"):
            deploy.deploy(DIGEST)
        self.assertEqual(self.env_file.read_text(), INITIAL_ENV.replace("keep", "updated") + previous_setting)
        self.assertEqual(self.fake.current, PREVIOUS)

    def test_http_200_cannot_mask_a_wrong_running_image(self):
        self.fake.wrong_image = True
        with self.assertRaisesRegex(deploy.DeploymentError, "previous live image restored"):
            deploy.deploy(DIGEST)
        self.assertEqual(self.fake.current, PREVIOUS)

    def test_public_failure_also_rolls_back(self):
        real_fake = self.fake

        def fail_public(*args, **kwargs):
            if args[0] == "curl" and args[-1].startswith("https://") and real_fake.current == CANDIDATE:
                return "502"
            return real_fake(*args, **kwargs)

        with patch.object(deploy, "run", side_effect=fail_public):
            with self.assertRaisesRegex(deploy.DeploymentError, "previous live image restored"):
                deploy.deploy(DIGEST)
        self.assertEqual(self.fake.current, PREVIOUS)

    def test_rollback_failure_is_reported(self):
        with patch.object(deploy, "wait_healthy", side_effect=deploy.DeploymentError("unhealthy")):
            with self.assertRaisesRegex(deploy.DeploymentError, "ROLLBACK FAILED"):
                deploy.deploy(DIGEST)

    def test_out_of_sync_baseline_does_not_change_live(self):
        self.fake.current = "different.example/image:old"
        with self.assertRaisesRegex(deploy.DeploymentError, "reconcile first"):
            deploy.deploy(DIGEST)
        self.assertEqual(self.env_file.read_text(), INITIAL_ENV)

    def test_termination_during_recreation_rolls_back(self):
        original_compose = deploy.compose
        interrupted = False

        def terminate_once(*args, **kwargs):
            nonlocal interrupted
            result = original_compose(*args, **kwargs)
            if args[0] == "up" and not interrupted:
                interrupted = True
                deploy.interrupted(deploy.signal.SIGTERM, None)
            return result

        with patch.object(deploy, "compose", side_effect=terminate_once):
            with self.assertRaisesRegex(deploy.DeploymentError, "previous live image restored"):
                deploy.deploy(DIGEST)
        self.assertEqual(self.fake.current, PREVIOUS)
        self.assertEqual(self.env_file.read_text(), INITIAL_ENV)

    def test_duplicate_live_settings_fail_without_mutation(self):
        content = INITIAL_ENV + "VVIVID_LIVE_IMAGE=one\nexport VVIVID_LIVE_IMAGE=two\n"
        self.env_file.write_text(content)
        with self.assertRaisesRegex(deploy.DeploymentError, "Duplicate"):
            deploy.deploy(DIGEST)
        self.assertEqual(self.env_file.read_text(), content)
        self.assertEqual(self.fake.events, [])


def locked_deployment(lock_path, trace_path, ready):
    """Run real deploy/flock in a separate process, with no real service commands."""
    deploy.LOCK_FILE = Path(lock_path)

    def record(phase):
        with open(trace_path, "a") as stream:
            stream.write(f"{os.getpid()} {phase}\n")

    def fake_health(*_args):
        record("start")
        time.sleep(0.15)
        record("end")

    with patch.object(deploy, "local_health_url", return_value=LOCAL_URL), \
            patch.object(deploy, "image_setting", return_value=None), \
            patch.object(deploy, "running_image", return_value=(PREVIOUS, PREVIOUS_ID)), \
            patch.object(deploy, "configured_image", side_effect=lambda image=None: image or PREVIOUS), \
            patch.object(deploy, "run", return_value=CANDIDATE_ID), \
            patch.object(deploy, "write_image_setting"), patch.object(deploy, "compose"), \
            patch.object(deploy, "wait_healthy", side_effect=fake_health):
        ready.wait(5)
        deploy.deploy(DIGEST)


class BoundaryTests(unittest.TestCase):
    def test_actual_host_lock_serializes_two_deployments(self):
        with tempfile.TemporaryDirectory() as directory:
            context = multiprocessing.get_context("fork")
            ready = context.Event()
            trace = Path(directory) / "trace"
            processes = [context.Process(target=locked_deployment,
                                         args=(str(Path(directory) / "lock"), str(trace), ready))
                         for _ in range(2)]
            for process in processes:
                process.start()
            ready.set()
            for process in processes:
                process.join(10)
                if process.is_alive():
                    process.kill()
                    process.join()
                self.assertEqual(process.exitcode, 0)
            events = [line.split() for line in trace.read_text().splitlines()]
            self.assertEqual([phase for _pid, phase in events], ["start", "end", "start", "end"])
            self.assertEqual(events[0][0], events[1][0])
            self.assertNotEqual(events[1][0], events[2][0])

    def test_health_endpoint_requires_trusted_file_and_http_origin(self):
        file = Mock()
        file.stat.return_value = Mock(st_uid=0, st_mode=0o100600)
        with patch.object(deploy, "HEALTH_URL_FILE", file):
            file.read_text.return_value = LOCAL_URL
            self.assertEqual(deploy.local_health_url(), LOCAL_URL)
            for value in ["https://example.com", "http://user:pass@example.com/", "http://example.com/other"]:
                file.read_text.return_value = value
                with self.assertRaises(deploy.DeploymentError):
                    deploy.local_health_url()
            file.stat.return_value.st_mode = 0o100664
            with self.assertRaises(deploy.DeploymentError):
                deploy.local_health_url()

    def test_ssh_entrypoint_rejects_shells_and_injection(self):
        specification = importlib.util.spec_from_file_location("ssh_entrypoint", Path(__file__).with_name("ssh-entrypoint.py"))
        entrypoint = importlib.util.module_from_spec(specification)
        specification.loader.exec_module(entrypoint)
        with patch.object(entrypoint.os, "execv") as execute:
            for command in ["", "sh", "deploy " + DIGEST + "; id", "deploy --help", "deploy " + DIGEST + "\n"]:
                with self.subTest(command=command), patch.dict(os.environ, {"SSH_ORIGINAL_COMMAND": command}):
                    self.assertEqual(entrypoint.main(), 1)
            execute.assert_not_called()
            with patch.dict(os.environ, {"SSH_ORIGINAL_COMMAND": "deploy " + DIGEST}):
                entrypoint.main()
            execute.assert_called_once_with("/usr/bin/sudo", ["sudo", "-n", "/usr/local/sbin/vvivid-live-deploy", DIGEST])


if __name__ == "__main__":
    unittest.main()
