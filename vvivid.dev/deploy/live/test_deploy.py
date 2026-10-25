"""Deployment contract tests. All Docker/HTTP commands are replaced by fakes."""

from contextlib import ExitStack
import importlib.util
import json
import multiprocessing
import os
from pathlib import Path
import sys
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


def load_preview_helper():
    specification = importlib.util.spec_from_file_location("preview_deploy", Path(deploy.__file__))
    helper = importlib.util.module_from_spec(specification)
    with patch.object(sys, "argv", ["/usr/local/sbin/vvivid-preview-deploy"]):
        specification.loader.exec_module(helper)
    return helper


PREVIEW_DEPLOY = load_preview_helper()


class FakeCommands:
    def __init__(self, env_file, helper):
        self.helper = helper
        self.env_file = env_file
        self.current = PREVIOUS
        self.image_id = PREVIOUS_ID
        self.events = []
        self.fail_pull = False
        self.fail_up = False
        self.fail_health = False
        self.wrong_image = False
        self.wrong_image_id = False
        self.supports_override = True
        self.change_other_setting = False

    def desired_image(self, override):
        if override is not None and self.supports_override:
            return override
        for line in self.env_file.read_text().splitlines():
            if line.startswith(self.helper.IMAGE_KEY + "="):
                return line.split("=", 1)[1]
        return PREVIOUS

    def __call__(self, *args, image=None, timeout=90):
        self.events.append((args, image))
        if args[:2] == ("docker", "compose"):
            expected = ("--project-name", "private-edge", "--env-file", str(self.env_file),
                        "--file", str(self.helper.COMPOSE_FILE))
            assert args[2:8] == expected
            command = args[8:]
            if command == ("config", "--format", "json"):
                return json.dumps({"services": {self.helper.SERVICE: {"image": self.desired_image(image)}}})
            if command == ("ps", "--quiet", "--status", "running", self.helper.SERVICE):
                return self.helper.SERVICE + "-container"
            assert command == ("up", "--detach", "--no-deps", "--pull", "never", self.helper.SERVICE)
            self.current = self.desired_image(image)
            self.image_id = CANDIDATE_ID if self.current == CANDIDATE else PREVIOUS_ID
            if self.fail_up:
                self.fail_up = False
                raise self.helper.DeploymentError("Fake partial Compose failure")
            return ""
        if args[:2] == ("docker", "pull"):
            assert args[2] == CANDIDATE
            assert timeout == 300
            if self.fail_pull:
                raise self.helper.DeploymentError("Fake registry denied")
            return ""
        if args[:3] == ("docker", "image", "inspect"):
            return CANDIDATE_ID
        if args[:2] == ("docker", "inspect"):
            current = PREVIOUS if self.wrong_image and self.current == CANDIDATE else self.current
            image_id = PREVIOUS_ID if self.wrong_image_id and self.current == CANDIDATE else self.image_id
            return f"{current}\n{image_id}\ntrue"
        assert args[0] == "curl", args
        assert f"Host: {self.helper.HOSTNAME}" in args
        assert "Cache-Control: no-cache" in args
        assert "--location" not in args
        if self.current == CANDIDATE and self.fail_health:
            if self.change_other_setting:
                content = self.env_file.read_text().replace("PREVIEW_FLAG=keep", "PREVIEW_FLAG=updated")
                self.env_file.write_text(content)
            return "503"
        return "200"


class DeploymentTests(unittest.TestCase):
    helper = deploy

    def setUp(self):
        other_key = "VVIVID_LIVE_IMAGE" if self.helper.SCOPE == "preview" else "VVIVID_PREVIEW_IMAGE"
        self.initial_env = INITIAL_ENV + f"{other_key}=other.example/site:unchanged\n"
        self.stack = ExitStack()
        self.addCleanup(self.stack.close)
        directory = Path(self.stack.enter_context(tempfile.TemporaryDirectory()))
        self.env_file = directory / ".env"
        self.env_file.write_text(self.initial_env)
        self.env_file.chmod(0o664)
        self.fake = FakeCommands(self.env_file, self.helper)
        self.stack.enter_context(patch.object(self.helper, "ENV_FILE", self.env_file))
        self.stack.enter_context(patch.object(self.helper, "LOCK_FILE", directory / "lock"))
        self.stack.enter_context(patch.object(self.helper, "local_health_url", return_value=LOCAL_URL))
        self.stack.enter_context(patch.object(self.helper, "run", side_effect=self.fake))
        self.stack.enter_context(patch.object(self.helper.time, "sleep"))
        self.stack.enter_context(patch.object(self.helper.signal, "signal"))
        writer = self.helper.write_image_setting

        def record_write(setting):
            self.fake.events.append((("write", setting), None))
            writer(setting)

        self.stack.enter_context(patch.object(self.helper, "write_image_setting", side_effect=record_write))

    def test_success_pulls_before_persistence_and_updates_only_own_service(self):
        self.helper.deploy(DIGEST)
        commands = [args for args, _ in self.fake.events]
        pull = next(index for index, args in enumerate(commands) if args[:2] == ("docker", "pull"))
        write = next(index for index, args in enumerate(commands) if args[0] == "write")
        self.assertLess(pull, write)
        self.assertEqual(self.env_file.read_text(), self.initial_env + f"{self.helper.IMAGE_KEY}={CANDIDATE}\n")
        self.assertEqual(self.env_file.stat().st_mode & 0o777, 0o664)
        self.assertEqual(self.fake.current, CANDIDATE)
        urls = [args[-1] for args in commands if args[0] == "curl"]
        self.assertTrue(urls[0].startswith(LOCAL_URL))
        self.assertTrue(urls[1].startswith(f"https://{self.helper.HOSTNAME}/"))

    def test_invalid_or_foreign_image_cannot_run_any_command(self):
        for value in ["latest", "SHA256:" + "a" * 64, DIGEST + "; id", "other.example/site@" + DIGEST]:
            with self.subTest(value=value), self.assertRaises(self.helper.DeploymentError):
                self.helper.deploy(value)
        self.assertEqual(self.fake.events, [])

    def test_failed_pull_preserves_container_and_persisted_settings(self):
        self.fake.fail_pull = True
        with self.assertRaisesRegex(self.helper.DeploymentError, "registry"):
            self.helper.deploy(DIGEST)
        self.assertEqual(self.env_file.read_text(), self.initial_env)
        self.assertEqual(self.fake.current, PREVIOUS)
        self.assertFalse(any(args[0] == "write" for args, _ in self.fake.events))

    def test_missing_compose_patch_fails_before_pulling_or_writing(self):
        self.fake.supports_override = False
        with self.assertRaisesRegex(self.helper.DeploymentError, f"separate {self.helper.SCOPE}-image"):
            self.helper.deploy(DIGEST)
        self.assertFalse(any(args[:2] == ("docker", "pull") for args, _ in self.fake.events))

    def test_partially_failed_compose_restores_previous_image_and_absent_key(self):
        self.fake.fail_up = True
        with self.assertRaisesRegex(self.helper.DeploymentError, f"previous {self.helper.SCOPE} image restored"):
            self.helper.deploy(DIGEST)
        self.assertEqual(self.fake.current, PREVIOUS)
        self.assertEqual(self.fake.image_id, PREVIOUS_ID)
        self.assertEqual(self.env_file.read_text(), self.initial_env)

    def test_failed_health_restores_previous_digest_without_overwriting_other_settings(self):
        previous_setting = f"{self.helper.IMAGE_KEY}={PREVIOUS}\n"
        self.env_file.write_text(self.initial_env + previous_setting)
        self.fake.fail_health = True
        self.fake.change_other_setting = True
        with self.assertRaisesRegex(self.helper.DeploymentError, f"previous {self.helper.SCOPE} image restored"):
            self.helper.deploy(DIGEST)
        self.assertEqual(self.env_file.read_text(), self.initial_env.replace("keep", "updated") + previous_setting)
        self.assertEqual(self.fake.current, PREVIOUS)

    def test_http_200_cannot_mask_a_wrong_running_image(self):
        self.fake.wrong_image = True
        with self.assertRaisesRegex(self.helper.DeploymentError, f"previous {self.helper.SCOPE} image restored"):
            self.helper.deploy(DIGEST)
        self.assertEqual(self.fake.current, PREVIOUS)

    def test_correct_reference_and_http_200_cannot_mask_a_wrong_image_id(self):
        self.fake.wrong_image_id = True
        with self.assertRaisesRegex(self.helper.DeploymentError, f"previous {self.helper.SCOPE} image restored"):
            self.helper.deploy(DIGEST)
        self.assertEqual(self.fake.current, PREVIOUS)

    def test_public_failure_also_rolls_back(self):
        real_fake = self.fake

        def fail_public(*args, **kwargs):
            if args[0] == "curl" and args[-1].startswith("https://") and real_fake.current == CANDIDATE:
                return "502"
            return real_fake(*args, **kwargs)

        with patch.object(self.helper, "run", side_effect=fail_public):
            with self.assertRaisesRegex(self.helper.DeploymentError, f"previous {self.helper.SCOPE} image restored"):
                self.helper.deploy(DIGEST)
        self.assertEqual(self.fake.current, PREVIOUS)

    def test_rollback_failure_is_reported(self):
        with patch.object(self.helper, "wait_healthy", side_effect=self.helper.DeploymentError("unhealthy")):
            with self.assertRaisesRegex(self.helper.DeploymentError, "ROLLBACK FAILED"):
                self.helper.deploy(DIGEST)

    def test_out_of_sync_baseline_does_not_change_service(self):
        self.fake.current = "different.example/image:old"
        with self.assertRaisesRegex(self.helper.DeploymentError, "reconcile first"):
            self.helper.deploy(DIGEST)
        self.assertEqual(self.env_file.read_text(), self.initial_env)

    def test_termination_during_recreation_rolls_back(self):
        original_compose = self.helper.compose
        interrupted = False

        def terminate_once(*args, **kwargs):
            nonlocal interrupted
            result = original_compose(*args, **kwargs)
            if args[0] == "up" and not interrupted:
                interrupted = True
                self.helper.interrupted(self.helper.signal.SIGTERM, None)
            return result

        with patch.object(self.helper, "compose", side_effect=terminate_once):
            with self.assertRaisesRegex(self.helper.DeploymentError, f"previous {self.helper.SCOPE} image restored"):
                self.helper.deploy(DIGEST)
        self.assertEqual(self.fake.current, PREVIOUS)
        self.assertEqual(self.env_file.read_text(), self.initial_env)

    def test_duplicate_image_settings_fail_without_mutation(self):
        content = self.initial_env + f"{self.helper.IMAGE_KEY}=one\nexport {self.helper.IMAGE_KEY}=two\n"
        self.env_file.write_text(content)
        with self.assertRaisesRegex(self.helper.DeploymentError, "Duplicate"):
            self.helper.deploy(DIGEST)
        self.assertEqual(self.env_file.read_text(), content)
        self.assertEqual(self.fake.events, [])


class PreviewDeploymentTests(DeploymentTests):
    helper = PREVIEW_DEPLOY


def locked_deployment(scope, env_path, lock_path, trace_path, ready, fail):
    """Exercise real flock and .env replacement across processes, with fake services."""
    helper = PREVIEW_DEPLOY if scope == "preview" else deploy
    helper.ENV_FILE = Path(env_path)
    helper.LOCK_FILE = Path(lock_path)

    def record(phase):
        with open(trace_path, "a") as stream:
            stream.write(f"{os.getpid()} {phase}\n")

    def fake_health(image, *_unused):
        record("start")
        time.sleep(0.15)
        record("end")
        if fail and image == CANDIDATE:
            raise helper.DeploymentError("Fake unhealthy candidate")

    with patch.object(helper, "local_health_url", return_value=LOCAL_URL), \
            patch.object(helper, "running_image", return_value=(PREVIOUS, PREVIOUS_ID)), \
            patch.object(helper, "configured_image", side_effect=lambda image=None: image or PREVIOUS), \
            patch.object(helper, "run", return_value=CANDIDATE_ID), patch.object(helper, "compose"), \
            patch.object(helper, "wait_healthy", side_effect=fake_health):
        ready.wait(5)
        try:
            helper.deploy(DIGEST)
        except helper.DeploymentError:
            if not fail:
                raise


class BoundaryTests(unittest.TestCase):
    def test_actual_host_lock_serializes_two_deployments(self):
        self.check_serialization(("live", "live"))

    def test_live_and_preview_updates_preserve_both_keys(self):
        self.check_serialization(("live", "preview"))

    def test_preview_rollback_cannot_erase_a_concurrent_live_update(self):
        self.check_serialization(("live", "preview"), fail_preview=True)

    def check_serialization(self, scopes, fail_preview=False):
        with tempfile.TemporaryDirectory() as directory:
            context = multiprocessing.get_context("fork")
            ready = context.Event()
            trace = Path(directory) / "trace"
            env_file = Path(directory) / ".env"
            env_file.write_text(INITIAL_ENV)
            processes = [context.Process(target=locked_deployment,
                                         args=(scope, str(env_file), str(Path(directory) / "lock"),
                                               str(trace), ready, fail_preview and scope == "preview"))
                         for scope in scopes]
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
            self.assertEqual([phase for _pid, phase in events], ["start", "end"] * (3 if fail_preview else 2))
            self.assertEqual(events[0][0], events[1][0])
            # A failed candidate's rollback holds the same lock until health returns.
            process_switches = sum(previous[0] != current[0]
                                   for previous, current in zip(events, events[1:]))
            self.assertEqual(process_switches, 1)
            content = env_file.read_text()
            self.assertTrue(content.startswith(INITIAL_ENV))
            self.assertEqual(content.count(f"VVIVID_LIVE_IMAGE={CANDIDATE}\n"), 1)
            self.assertEqual(content.count(f"VVIVID_PREVIEW_IMAGE={CANDIDATE}\n"),
                             int("preview" in scopes and not fail_preview))

    def test_installed_names_have_fixed_separate_scopes_and_one_lock(self):
        for helper, scope, hostname in ((deploy, "live", "vvivid.dev"),
                                        (PREVIEW_DEPLOY, "preview", "preview.vvivid.dev")):
            with self.subTest(scope=scope):
                self.assertEqual(helper.SERVICE, "vvivid-" + scope)
                self.assertEqual(helper.IMAGE_KEY, "VVIVID_" + scope.upper() + "_IMAGE")
                self.assertEqual(helper.HEALTH_URL_FILE, Path(f"/etc/vvivid-{scope}/health-url"))
                self.assertEqual(helper.HOSTNAME, hostname)
                self.assertEqual(helper.LOCK_FILE, Path("/run/lock/vvivid-deployment.lock"))
                with patch.object(helper.subprocess, "run", return_value=Mock(returncode=0, stdout="")) as command:
                    helper.run("docker", "compose", image=CANDIDATE)
                environment = command.call_args.kwargs["env"]
                self.assertEqual(environment[helper.IMAGE_KEY], CANDIDATE)
                self.assertEqual(set(environment), {"PATH", "DOCKER_CONFIG", helper.IMAGE_KEY})

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
        for scope in ("live", "preview"):
            path = Path(__file__).resolve().parents[1] / scope / "ssh-entrypoint.py"
            specification = importlib.util.spec_from_file_location("ssh_entrypoint", path)
            entrypoint = importlib.util.module_from_spec(specification)
            specification.loader.exec_module(entrypoint)
            with patch.object(entrypoint.os, "execv") as execute:
                for command in ["", "sh", "deploy " + DIGEST + "; id", "deploy --help",
                                "deploy " + DIGEST + "\n", "deploy live " + DIGEST,
                                "deploy preview " + DIGEST]:
                    with self.subTest(scope=scope, command=command), patch.dict(os.environ, {"SSH_ORIGINAL_COMMAND": command}):
                        self.assertEqual(entrypoint.main(), 1)
                execute.assert_not_called()
                with patch.dict(os.environ, {"SSH_ORIGINAL_COMMAND": "deploy " + DIGEST}):
                    entrypoint.main()
                execute.assert_called_once_with("/usr/bin/sudo", ["sudo", "-n", f"/usr/local/sbin/vvivid-{scope}-deploy", DIGEST])


if __name__ == "__main__":
    unittest.main()
