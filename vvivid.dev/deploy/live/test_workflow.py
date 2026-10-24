"""Execute the workflow's deployment gate with a fake GitHub CLI and no network."""

import json
import os
from pathlib import Path
import subprocess
import tempfile
import textwrap
import unittest


SOURCE_SHA = "a" * 40
MAIN_SHA = "b" * 40
DIGEST = "sha256:" + "c" * 64


class WorkflowGateTests(unittest.TestCase):
    def run_gate(self, *, main=MAIN_SHA, changed=None, fail=None, digest=DIGEST):
        workflow = Path(__file__).resolve().parents[3] / ".github/workflows/website.yml"
        gate = workflow.read_text().split("      - name: Validate digest and skip superseded main runs\n", 1)[1]
        gate = gate.split("        run: |\n", 1)[1].split("      - name:", 1)[0]
        gate = textwrap.dedent(gate)
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
                if path.endswith('/commits/main'):
                    print(scenario['main'])
                elif '/git/trees/' in path:
                    print(('2' if scenario['changed'] == 'site' and path.endswith(scenario['main']) else '1') * 40)
                elif '/contents/.github/workflows/website.yml?ref=' in path:
                    print(('4' if scenario['changed'] == 'workflow' and path.endswith(scenario['main']) else '3') * 40)
                else:
                    raise AssertionError(path)
                """))
            fake_gh.chmod(0o755)
            scenario = root / "scenario.json"
            scenario.write_text(json.dumps({"main": main, "changed": changed, "fail": fail}))
            output = root / "output"
            summary = root / "summary"
            output.touch()
            summary.touch()
            environment = dict(os.environ, PATH=str(root) + os.pathsep + os.environ["PATH"],
                               SCENARIO=str(scenario), SOURCE_SHA=SOURCE_SHA,
                               GITHUB_REPOSITORY="VVivid/vvivid-workspace", BUILD_DIGEST=digest,
                               REQUESTED_DIGEST="", GITHUB_OUTPUT=str(output), GITHUB_STEP_SUMMARY=str(summary))
            result = subprocess.run(["bash", "--noprofile", "--norc", "-e", "-o", "pipefail", "-c", gate],
                                    env=environment, capture_output=True, text=True, timeout=10)
            return result.returncode, output.read_text(), summary.read_text()

    def test_current_main_is_allowed(self):
        code, output, _summary = self.run_gate(main=SOURCE_SHA)
        self.assertEqual(code, 0)
        self.assertEqual(output, f"digest={DIGEST}\n")

    def test_unrelated_main_commit_keeps_valid_website_deployment(self):
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
        for failure in ["/commits/main", "/git/trees/", "/contents/"]:
            with self.subTest(failure=failure):
                code, output, _summary = self.run_gate(fail=failure)
                self.assertNotEqual(code, 0)
                self.assertEqual(output, "")

    def test_invalid_digest_is_rejected(self):
        code, output, _summary = self.run_gate(digest="latest")
        self.assertNotEqual(code, 0)
        self.assertEqual(output, "")


if __name__ == "__main__":
    unittest.main()
