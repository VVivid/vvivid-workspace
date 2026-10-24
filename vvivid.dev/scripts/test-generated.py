#!/usr/bin/env python3
"""Check publication rules against a temporary copy of the production source."""

import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
from urllib.parse import urlsplit


class GeneratedContentTests(unittest.TestCase):
    def test_published_snapshot_and_skill_integrity(self):
        project = Path(__file__).resolve().parents[1]
        with tempfile.TemporaryDirectory(prefix="vvivid-content-") as temporary:
            site = Path(temporary) / "site"
            shutil.copytree(project, site, ignore=shutil.ignore_patterns(
                "public", "resources", ".development", ".hugo_build.lock", "__pycache__"))
            fixtures = {
                "published": "date: 2001-01-01\npublishDate: 2002-01-01\nlastmod: 2003-01-01",
                "draft": "date: 2001-01-01\ndraft: true",
                "future": "date: 2999-01-01",
                "scheduled": "date: 2001-01-01\npublishDate: 2999-01-01",
            }
            for name, metadata in fixtures.items():
                (site / "content/posts" / f"fixture-{name}.md").write_text(
                    f'---\ntitle: "Fixture {name}"\n{metadata}\n'
                    'description: "Quoted metadata: café & tests"\n'
                    'tags: ["fixture"]\n---\n\n```go\nfmt.Println("fixture")\n```\n',
                    encoding="utf-8")
            build_environment = dict(os.environ, HUGO_BASEURL="https://preview.example/", HUGO_DESTINATION="public")
            built = subprocess.run(["./scripts/build.sh"], cwd=site, env=build_environment,
                                   text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
            self.assertEqual(built.returncode, 0, built.stdout)
            output = site / "public"
            content = json.loads((output / "api/content.json").read_bytes())
            self.assertEqual(content["count"], len(content["items"]))
            fixture = next(item for item in content["items"] if item["title"] == "Fixture published")
            self.assertEqual(fixture["description"], "Quoted metadata: café & tests")
            self.assertEqual(fixture["tags"], ["fixture"])
            self.assertEqual(fixture["published_at"], "2002-01-01T00:00:00Z")
            self.assertEqual(fixture["modified_at"], "2003-01-01T00:00:00Z")
            self.assertEqual(fixture["url"], "https://preview.example/posts/fixture-published/")
            markdown = output / urlsplit(fixture["markdown_url"]).path.lstrip("/")
            self.assertIn('```go\nfmt.Println("fixture")\n```', markdown.read_text())
            about = next(item for item in content["items"] if urlsplit(item["url"]).path == "/about/")
            self.assertNotIn("published_at", about)
            self.assertNotIn("modified_at", about)
            self.assertEqual(about["tags"], [])
            for name in ("draft", "future", "scheduled"):
                marker = f"fixture-{name}"
                for resource in ("api/content.json", "llms.txt", "sitemap.xml"):
                    self.assertNotIn(marker, (output / resource).read_text(), resource)
                self.assertFalse((output / "posts" / marker).exists())
            catalog = json.loads((output / ".well-known/api-catalog").read_bytes())
            self.assertEqual(catalog["linkset"][0]["anchor"], "https://preview.example/api/content.json")
            specification = json.loads((output / "api/openapi.json").read_bytes())
            self.assertEqual(specification["servers"], [{"url": "https://preview.example"}])
            index = json.loads((output / ".well-known/agent-skills/index.json").read_bytes())
            skill = index["skills"][0]
            artifact = (output / skill["url"].lstrip("/")).read_bytes()
            self.assertEqual(skill["digest"], "sha256:" + hashlib.sha256(artifact).hexdigest())
            self.assertIn(b"https://preview.example/api/content.json", artifact)
            self.assertTrue(artifact.startswith(f'---\nname: {skill["name"]}\n'.encode()))
            self.assertIn(f'description: {skill["description"]}\n'.encode(), artifact)
            self.assertFalse(any(path.name.lower() in ("agent.md", "agents.md") for path in output.rglob("*")))


if __name__ == "__main__":
    unittest.main(verbosity=2)
