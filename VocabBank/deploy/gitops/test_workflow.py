"""Check that the workflow wires the reviewed project contracts together."""
from pathlib import Path
import re
import unittest

import yaml

ROOT = Path(__file__).resolve().parents[3]


class WorkflowTests(unittest.TestCase):
    def test_project_filters_tools_and_required_checks(self):
        workflow = yaml.safe_load((ROOT / ".github/workflows/vocabbank.yml").read_text())
        triggers = workflow.get("on", workflow.get(True))
        paths = ["VocabBank/**", ".github/workflows/vocabbank.yml", ".github/scripts/install-vocabbank-tools.sh", "Makefile"]
        self.assertEqual(triggers["pull_request"]["paths"], paths)
        self.assertEqual(triggers["push"]["paths"], paths)
        self.assertEqual(triggers["push"]["branches"], ["main"])
        self.assertEqual(triggers["push"]["tags"], ["VocabBank/v*"])
        jobs = workflow["jobs"]
        check = jobs["check"]
        text = "\n".join(step.get("run", "") for step in check["steps"])
        for command in ["make -C VocabBank deps", "make -C VocabBank check test race build",
                        "make -C VocabBank browser-install browser-test", "make -C VocabBank check test",
                        "make -C VocabBank chart-test", "--platform linux/amd64", "make -C VocabBank container-test",
                        "playwright install-deps chromium"]:
            self.assertIn(command, text)
        self.assertGreater(text.index("make -C VocabBank check test\n"), text.index("make -C VocabBank browser-install browser-test"))
        node = next(step for step in check["steps"] if "actions/setup-node@" in step.get("uses", ""))
        self.assertEqual(node["with"]["cache-dependency-path"], "VocabBank/web/package-lock.json")
        for job in jobs.values():
            for step in job["steps"]:
                if "uses" in step:
                    self.assertRegex(step["uses"], r"^[a-zA-Z0-9/_-]+@[0-9a-f]{40}$")
                for helper in re.findall(r"(?:bash|python3) (\.github/[^\s]+|VocabBank/[^\s]+\.py)", step.get("run", "")):
                    self.assertTrue((ROOT / helper).is_file(), helper)

    def test_publication_is_gated_and_exercises_candidate_before_tailnet(self):
        publish = yaml.safe_load((ROOT / ".github/workflows/vocabbank.yml").read_text())["jobs"]["publish"]
        self.assertEqual(publish["needs"], "check")
        self.assertIn("startsWith(github.ref, 'refs/tags/VocabBank/v')", publish["if"])
        self.assertIn("vars.VOCAB_GITOPS_ENABLED == 'true'", publish["if"])
        self.assertEqual(publish["environment"], "artifact-publication")
        self.assertEqual(publish["concurrency"], {"group": "monorepo-artifact-publication", "cancel-in-progress": False})
        self.assertEqual(publish["permissions"], {"contents": "read", "id-token": "write"})
        steps = publish["steps"]
        run_text = "\n".join(step.get("run", "") for step in steps)
        self.assertIn('git merge-base --is-ancestor "$SOURCE_SHA" refs/remotes/origin/main', run_text)
        test_index = next(index for index, step in enumerate(steps) if "container-test TEST_IMAGE=vocabbank:release-check" in step.get("run", ""))
        tailnet_index = next(index for index, step in enumerate(steps) if "tailscale/github-action@" in step.get("uses", ""))
        self.assertLess(test_index, tailnet_index)
        self.assertIn("--plan", run_text)
        self.assertIn("--build-arg", run_text)
        identity = steps[tailnet_index]["with"]
        self.assertEqual(identity, {"oauth-client-id": "${{ vars.TS_CLIENT_ID }}", "audience": "${{ vars.TS_AUDIENCE }}", "tags": "${{ vars.TS_CI_TAG }}"})
        app = next(step for step in steps if "actions/create-github-app-token@" in step.get("uses", ""))
        self.assertEqual(app["with"]["repositories"], "homelab-gitops")
        self.assertEqual(app["with"]["permission-contents"], "write")


if __name__ == "__main__":
    unittest.main()
