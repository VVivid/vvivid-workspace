"""Exercise both workflows' routing, build commands, and stale-run gates locally."""

import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import textwrap
import unittest


SOURCE_SHA = "a" * 40
NEWER_SHA = "b" * 40
DIGEST = "sha256:" + "c" * 64
WORKFLOWS = Path(__file__).resolve().parents[3] / ".github/workflows"


def workflow(scope):
    return (WORKFLOWS / ("website-preview.yml" if scope == "preview" else "website.yml")).read_text()


def job(scope, name):
    sections = re.split(r"^  ([a-z-]+):\n", workflow(scope).split("jobs:\n", 1)[1], flags=re.MULTILINE)
    return dict(zip(sections[1::2], sections[2::2]))[name]


def step_script(scope, job_name, step_name):
    step = job(scope, job_name).split(f"      - name: {step_name}\n", 1)[1]
    return textwrap.dedent(step.split("        run: |\n", 1)[1].split("      - name:", 1)[0])


def job_enabled(scope, name, *, event="push", ref="refs/heads/main", digest="", build="success"):
    """Evaluate the small, explicit GitHub condition subset used by these jobs."""
    condition = re.search(r"^    if: (.+)(?:\n((?:      .+\n)*))", job(scope, name), re.MULTILINE)
    expression = condition[1] if condition[1] != ">-" else " ".join(condition[2].split())
    values = {"github.event_name": event, "github.ref": ref, "inputs.image_digest": digest,
              "needs.build.result": build, "always()": True, "cancelled()": False}
    for key, value in values.items():
        expression = expression.replace(key, repr(value))
    expression = expression.replace("&&", " and ").replace("||", " or ").replace("!False", "not False")
    return eval(expression, {"__builtins__": {}}, {})


class WorkflowRoutingTests(unittest.TestCase):
    def test_only_exact_branch_builds_or_deploys_each_scope(self):
        for scope, branch in (("live", "main"), ("preview", "release/vvivid-preview")):
            expected = "refs/heads/" + branch
            for ref in ("refs/heads/main", "refs/heads/release/vvivid-preview", "refs/heads/release/other",
                        "refs/heads/release/vvivid-preview-extra", "refs/heads/feature/website/preview",
                        "refs/tags/release/vvivid-preview", "refs/tags/vvivid.dev/v1.0.0"):
                for event in ("push", "workflow_dispatch", "pull_request"):
                    with self.subTest(scope=scope, ref=ref, event=event):
                        allowed = ref == expected and event != "pull_request"
                        self.assertEqual(job_enabled(scope, "build", event=event, ref=ref), allowed)
                        self.assertEqual(job_enabled(scope, "deploy", event=event, ref=ref), allowed)
                        self.assertEqual(job_enabled(scope, "pull-request", event=event, ref=ref), event == "pull_request")

    def test_explicit_rollback_skips_build_and_requires_success_or_requested_digest(self):
        for scope, ref in (("live", "refs/heads/main"), ("preview", "refs/heads/release/vvivid-preview")):
            with self.subTest(scope=scope):
                self.assertFalse(job_enabled(scope, "build", event="workflow_dispatch", ref=ref, digest=DIGEST))
                self.assertTrue(job_enabled(scope, "deploy", event="workflow_dispatch", ref=ref, digest=DIGEST, build="skipped"))
                self.assertFalse(job_enabled(scope, "deploy", ref=ref, build="failure"))
                self.assertFalse(job_enabled(scope, "deploy", ref=ref, build="skipped"))

    def test_preview_first_branch_push_and_workflow_changes_are_not_path_filtered(self):
        push = workflow("preview").split("  push:\n", 1)[1].split("  workflow_dispatch:", 1)[0]
        self.assertIn("branches: [release/vvivid-preview]", push)
        self.assertNotRegex(push, r"(?m)^    paths(?:-ignore)?:")
        live_push = workflow("live").split("  push:\n", 1)[1].split("  workflow_dispatch:", 1)[0]
        self.assertIn("branches: [main]", live_push)
        self.assertIn("'vvivid.dev/**'", live_push)
        self.assertIn("'.github/workflows/website.yml'", live_push)
        for scope in ("live", "preview"):
            pull_request = workflow(scope).split("  pull_request:\n", 1)[1].split("  push:", 1)[0]
            for path in ("vvivid.dev/**", ".github/workflows/website.yml", ".github/workflows/website-preview.yml"):
                self.assertIn(f"'{path}'", pull_request)

    def test_preview_has_dedicated_deployment_identity_and_no_indexnow_submission(self):
        for scope, hostname, account in (("live", "vvivid.dev", "vvivid-deploy"),
                                          ("preview", "preview.vvivid.dev", "vvivid-preview-deploy")):
            with self.subTest(scope=scope):
                deployment = job(scope, "deploy")
                self.assertIn(f"      name: vvivid-{scope}\n      url: https://{hostname}/", deployment)
                self.assertIn(f"      group: vvivid-{scope}\n      cancel-in-progress: false", deployment)
                self.assertIn(f"tags: tag:vvivid-{scope}-ci", deployment)
                self.assertIn(f'"{account}@$DEPLOY_HOST" "deploy $IMAGE_DIGEST"', deployment)
                self.assertIn("-o StrictHostKeyChecking=yes", deployment)
                self.assertNotIn("packages: write", deployment)
                checks = job(scope, "pull-request")
                for elevated in ("packages: write", "id-token: write", "secrets.", "environment:"):
                    self.assertNotIn(elevated, checks)
                self.assertIn("make -C vvivid.dev deployment-test", checks)
                self.assertNotRegex(workflow(scope), r"(?<!test-)indexnow\.py|--submit")

    def test_build_commands_keep_live_and_preview_images_distinct(self):
        for scope in ("live", "preview"):
            with self.subTest(scope=scope), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                log = root / "commands"
                recorder_script = textwrap.dedent("""\
                    #!/usr/bin/env python3
                    import json, os, sys
                    from pathlib import Path
                    with Path(os.environ['COMMAND_LOG']).open('a') as stream:
                        stream.write(json.dumps(sys.argv) + '\\n')
                    """)
                fake_docker = root / "docker"
                fake_docker.write_text(recorder_script)
                fake_docker.chmod(0o755)
                fake_smoke = root / "vvivid.dev/scripts/smoke-image.sh"
                fake_smoke.parent.mkdir(parents=True)
                fake_smoke.write_text(recorder_script)
                fake_smoke.chmod(0o755)
                script = step_script(scope, "build", "Build and check the complete serving image")
                environment = dict(os.environ, PATH=str(root) + os.pathsep + os.environ["PATH"],
                                   COMMAND_LOG=str(log), SOURCE_SHA=SOURCE_SHA,
                                   IMAGE_REPOSITORY="ghcr.io/vvivid/vvivid-website", GITHUB_REPOSITORY="VVivid/vvivid-workspace")
                subprocess.run(["bash", "-e", "-o", "pipefail", "-c", script], cwd=root,
                               env=environment, check=True, capture_output=True, timeout=10)
                build, smoke_call = [json.loads(line) for line in log.read_text().splitlines()]
                tag = f"ghcr.io/vvivid/vvivid-website:{'preview-' if scope == 'preview' else ''}sha-{SOURCE_SHA}"
                self.assertEqual(build[build.index("--tag") + 1], tag)
                self.assertEqual(smoke_call[1], tag)
                if scope == "preview":
                    self.assertEqual(build[build.index("--build-arg") + 1], "HUGO_BASEURL=https://preview.vvivid.dev/")
                    self.assertEqual(smoke_call[2], "https://preview.vvivid.dev/")
                else:
                    self.assertNotIn("--build-arg", build)
                    self.assertEqual(smoke_call[2], "https://vvivid.dev/")
                publish = step_script(scope, "build", "Publish immutable image digest")
                self.assertIn(f'image="$IMAGE_REPOSITORY:{"preview-" if scope == "preview" else ""}sha-$SOURCE_SHA"', publish)


class WorkflowGateTests(unittest.TestCase):
    scope = "live"

    def run_gate(self, *, current=NEWER_SHA, changed=None, fail=None, digest=DIGEST, requested=""):
        gate_scope = "preview" if self.scope == "preview" else "main"
        gate_name = f"Validate digest and skip superseded {gate_scope} runs"
        script = step_script(self.scope, "deploy", gate_name)
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            fake_gh = root / "gh"
            fake_gh.write_text("#!/usr/bin/env python3\n" + textwrap.dedent("""\
                import json, os, sys
                from pathlib import Path
                scenario = json.loads(Path(os.environ['SCENARIO']).read_text())
                path = sys.argv[2]
                if scenario['fail'] and scenario['fail'] in path:
                    sys.exit(7)
                if path.endswith('/commits/' + scenario['branch']):
                    print(scenario['current'])
                elif '/git/trees/' in path:
                    print(('2' if scenario['changed'] == 'site' and path.endswith(scenario['current']) else '1') * 40)
                elif '/contents/.github/workflows/' + scenario['workflow'] + '?ref=' in path:
                    print(('4' if scenario['changed'] == 'workflow' and path.endswith(scenario['current']) else '3') * 40)
                else:
                    raise AssertionError(path)
                """))
            fake_gh.chmod(0o755)
            scenario = root / "scenario.json"
            scenario.write_text(json.dumps({"current": current, "changed": changed, "fail": fail,
                                             "branch": "release%2Fvvivid-preview" if self.scope == "preview" else "main",
                                             "workflow": "website-preview.yml" if self.scope == "preview" else "website.yml"}))
            output = root / "output"
            summary = root / "summary"
            output.touch()
            summary.touch()
            environment = dict(os.environ, PATH=str(root) + os.pathsep + os.environ["PATH"],
                               SCENARIO=str(scenario), SOURCE_SHA=SOURCE_SHA,
                               GITHUB_REPOSITORY="VVivid/vvivid-workspace", BUILD_DIGEST=digest,
                               REQUESTED_DIGEST=requested, GITHUB_OUTPUT=str(output), GITHUB_STEP_SUMMARY=str(summary))
            result = subprocess.run(["bash", "--noprofile", "--norc", "-e", "-o", "pipefail", "-c", script],
                                    env=environment, capture_output=True, text=True, timeout=10)
            return result.returncode, output.read_text(), summary.read_text()

    def test_current_branch_is_allowed(self):
        code, output, _summary = self.run_gate(current=SOURCE_SHA)
        self.assertEqual(code, 0)
        self.assertEqual(output, f"digest={DIGEST}\n")

    def test_unrelated_monorepo_commit_keeps_valid_website_deployment(self):
        code, output, _summary = self.run_gate()
        self.assertEqual(code, 0)
        self.assertEqual(output, f"digest={DIGEST}\n")

    def test_newer_website_or_workflow_skips_old_run(self):
        for changed in ["site", "workflow"]:
            with self.subTest(changed=changed):
                code, output, summary = self.run_gate(changed=changed)
                self.assertEqual(code, 0)
                self.assertEqual(output, "")
                self.assertIn("Skipped", summary)

    def test_api_failure_cannot_bypass_gate(self):
        for failure in ["/commits/", "/git/trees/", "/contents/"]:
            with self.subTest(failure=failure):
                code, output, _summary = self.run_gate(fail=failure)
                self.assertNotEqual(code, 0)
                self.assertEqual(output, "")

    def test_invalid_digest_is_rejected(self):
        for digest in ("latest", "sha256:" + "A" * 64, DIGEST + "; id"):
            with self.subTest(digest=digest):
                code, output, _summary = self.run_gate(digest=digest)
                self.assertNotEqual(code, 0)
                self.assertEqual(output, "")

    def test_requested_rollback_digest_takes_precedence_and_is_validated(self):
        code, output, _summary = self.run_gate(requested=DIGEST, digest="")
        self.assertEqual(code, 0)
        self.assertEqual(output, f"digest={DIGEST}\n")
        code, output, _summary = self.run_gate(requested="latest")
        self.assertNotEqual(code, 0)
        self.assertEqual(output, "")


class PreviewWorkflowGateTests(WorkflowGateTests):
    scope = "preview"


if __name__ == "__main__":
    unittest.main()
