"""Keep CI gates and publication credentials on the intended trust boundary."""

from pathlib import Path
import subprocess
import unittest

import yaml

ROOT = Path(__file__).resolve().parents[3]


class WorkflowContractTests(unittest.TestCase):
    def setUp(self):
        self.workflow = yaml.safe_load((ROOT / '.github/workflows/shopping.yml').read_text())

    def test_checks_run_for_pull_requests_main_and_project_tags(self):
        # PyYAML's YAML 1.1 loader reads the GitHub `on` key as True.
        events = self.workflow[True]
        self.assertEqual(events['push'], {'branches': ['main'], 'tags': ['AgentShoppingList/v*']})
        self.assertIn('AgentShoppingList/**', events['pull_request']['paths'])
        check = self.workflow['jobs']['check']
        self.assertEqual(self.workflow['permissions'], {'contents': 'read'})
        self.assertNotIn('environment', check)
        commands = '\n'.join(step.get('run', '') for step in check['steps'])
        for required in ('npm ci --ignore-scripts', 'assets check race build', 'chart-test',
                         'docker build --platform linux/amd64', 'container-test'):
            self.assertIn(required, commands)
        self.assertNotIn('secrets.', str(check))
        self.assertNotIn('tailscale/', str(check))

    def test_publication_reuses_shared_lock_environment_and_scoped_identities(self):
        publish = self.workflow['jobs']['publish']
        self.assertEqual(publish['needs'], 'check')
        for condition in ("github.event_name == 'push'", "refs/tags/AgentShoppingList/v", "vars.SHOPPING_GITOPS_ENABLED == 'true'"):
            self.assertIn(condition, publish['if'])
        self.assertEqual(publish['environment'], 'artifact-publication')
        self.assertEqual(publish['concurrency'], {'group': 'monorepo-artifact-publication', 'cancel-in-progress': False})
        self.assertEqual(publish['permissions'], {'contents': 'read', 'id-token': 'write'})
        steps = publish['steps']
        tailnet_index = next(i for i, step in enumerate(steps) if step.get('uses', '').startswith('tailscale/'))
        earlier = '\n'.join(step.get('run', '') for step in steps[:tailnet_index])
        self.assertIn('git merge-base --is-ancestor "$SOURCE_SHA" refs/remotes/origin/main', earlier)
        self.assertIn('container-test TEST_IMAGE=agentshoppinglist:release-check', earlier)
        tailnet = steps[tailnet_index]['with']
        self.assertEqual(set(tailnet), {'oauth-client-id', 'audience', 'tags'})
        app = next(step for step in steps if step.get('id') == 'gitops-token')['with']
        self.assertEqual(app['repositories'], 'homelab-gitops')
        self.assertEqual(app['permission-contents'], 'write')
        commands = '\n'.join(step.get('run', '') for step in steps)
        self.assertIn('ZOT_PRODUCTS_PUSH', str(steps))
        self.assertIn('--password-stdin', commands)
        self.assertIn('promote.py --ref "$SOURCE_REF" --sha "$SOURCE_SHA"', commands)
        self.assertNotIn('kubectl', commands)
        for job in self.workflow['jobs'].values():
            for step in job['steps']:
                if 'uses' in step:
                    self.assertRegex(step['uses'], r'^[^@]+@[0-9a-f]{40}$')

    def test_container_target_preserves_existing_and_new_image_selection(self):
        for arguments, expected in (([], "agentshoppinglist:local"),
                                    (["SHOPPING_TEST_IMAGE=existing:checked"], "existing:checked"),
                                    (["TEST_IMAGE=release:checked"], "release:checked")):
            command = subprocess.run(["make", "--no-print-directory", "-n", "container-test", *arguments],
                                     cwd=ROOT / "AgentShoppingList", text=True, capture_output=True, check=True)
            self.assertIn("SHOPPING_TEST_IMAGE=" + expected + " sh scripts/test-container.sh", command.stdout)

    def test_release_tools_keep_the_maintained_checksum_pin(self):
        installer = (ROOT / '.github/scripts/install-shopping-tools.sh').read_text()
        self.assertIn('helm-v4.3.1-linux-amd64.tar.gz', installer)
        self.assertIn('0403a88ae20913d11b10a2467b50087721fea7581736da6b9d3992f5357fe54b', installer)
        self.assertIn('sha256sum --check', installer)


if __name__ == '__main__':
    unittest.main()
