#!/usr/bin/env python3
"""Check publication, index eligibility, and metadata in isolated Hugo builds."""

from datetime import datetime
import hashlib
from html.parser import HTMLParser
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
from urllib.parse import urlsplit
import xml.etree.ElementTree as ET


class Metadata(HTMLParser):
    def __init__(self, html):
        super().__init__()
        self.meta = {}
        self.links = []
        self.schemas = []
        self.headings = []
        self.byline = False
        self.in_schema = False
        self.feed(html)

    def handle_starttag(self, tag, attrs):
        fields = dict(attrs)
        if tag == "meta":
            self.meta[fields.get("name", fields.get("property"))] = fields.get("content")
        if tag == "link":
            self.links.append(fields)
        if tag in ("h1", "h2", "h3", "h4"):
            self.headings.append(int(tag[1]))
        if tag == "a" and fields.get("rel") == "author":
            self.byline = fields.get("href") == "/about/"
        if tag == "script":
            self.in_schema = fields.get("type") == "application/ld+json"

    def handle_endtag(self, tag):
        if tag == "script":
            self.in_schema = False

    def handle_data(self, data):
        if self.in_schema:
            self.schemas.append(json.loads(data))


class GeneratedContentTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temporary = tempfile.TemporaryDirectory(prefix="vvivid-content-")
        cls.addClassCleanup(cls.temporary.cleanup)
        cls.site = Path(cls.temporary.name) / "site"
        project = Path(__file__).resolve().parents[1]
        shutil.copytree(project, cls.site, ignore=shutil.ignore_patterns(
            "public", "resources", ".development", ".hugo_build.lock", "__pycache__"))
        fixtures = {
            "published": "date: 2001-01-01\npublishDate: 2002-01-01\nlastmod: 2003-01-01",
            "draft": "date: 2001-01-01\ndraft: true",
            "future": "date: 2999-01-01",
            "scheduled": "date: 2001-01-01\npublishDate: 2999-01-01",
        }
        for name, metadata in fixtures.items():
            (cls.site / "content/posts" / f"fixture-{name}.md").write_text(
                f'---\ntitle: "Fixture {name}"\n{metadata}\n'
                'description: "Quoted metadata: café & tests"\n'
                'tags: ["fixture"]\nmermaid: true\n---\n\n'
                'An ordinary code block does not need a diagram renderer.\n\n'
                '## Example\n\n```go\nfmt.Println("fixture")\n```\n', encoding="utf-8")
        term = cls.site / "content/tags/fixture"
        term.mkdir()
        (term / "_index.md").write_text('---\ntitle: Fixture\nindex: true\n---\nA curated but single-item archive.\n')
        cls.outputs = {}
        for name, base in (("production", "https://vvivid.dev/"), ("preview", "https://preview.vvivid.dev/")):
            output = Path(cls.temporary.name) / name
            environment = dict(os.environ, HUGO_BASEURL=base, HUGO_DESTINATION=str(output))
            built = subprocess.run(["./scripts/build.sh"], cwd=cls.site, env=environment,
                                   text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
            if built.returncode:
                raise RuntimeError(built.stdout)
            cls.outputs[name] = output

    def read(self, path, build="production"):
        return (self.outputs[build] / path).read_text(encoding="utf-8")

    def sitemap(self, build="production"):
        return [node.text for node in ET.fromstring(self.read("sitemap.xml", build)).findall("{*}url/{*}loc")]

    def test_published_snapshot_and_skill_integrity(self):
        content = json.loads(self.read("api/content.json", "preview"))
        self.assertEqual(content["count"], len(content["items"]))
        fixture = next(item for item in content["items"] if item["title"] == "Fixture published")
        self.assertEqual(fixture["description"], "Quoted metadata: café & tests")
        self.assertEqual(fixture["tags"], ["fixture"])
        self.assertEqual(fixture["published_at"], "2002-01-01T00:00:00Z")
        self.assertEqual(fixture["modified_at"], "2003-01-01T00:00:00Z")
        self.assertEqual(fixture["url"], "https://preview.vvivid.dev/posts/fixture-published/")
        markdown = urlsplit(fixture["markdown_url"]).path.lstrip("/")
        self.assertIn('```go\nfmt.Println("fixture")\n```', self.read(markdown, "preview"))
        about = next(item for item in content["items"] if urlsplit(item["url"]).path == "/about/")
        self.assertNotIn("published_at", about)
        self.assertNotIn("modified_at", about)
        self.assertEqual(about["tags"], [])
        for build in self.outputs:
            for name in ("draft", "future", "scheduled"):
                marker = f"fixture-{name}"
                for resource in ("api/content.json", "llms.txt", "sitemap.xml"):
                    self.assertNotIn(marker, self.read(resource, build), resource)
                self.assertFalse((self.outputs[build] / "posts" / marker).exists())
            index = json.loads(self.read(".well-known/agent-skills/index.json", build))
            skill = index["skills"][0]
            artifact = (self.outputs[build] / skill["url"].lstrip("/")).read_bytes()
            self.assertEqual(skill["digest"], "sha256:" + hashlib.sha256(artifact).hexdigest())
            self.assertTrue(artifact.startswith(f'---\nname: {skill["name"]}\n'.encode()))
            self.assertIn(f'description: {skill["description"]}\n'.encode(), artifact)
            self.assertFalse(any(path.name.lower() in ("agent.md", "agents.md") for path in self.outputs[build].rglob("*")))
        catalog = json.loads(self.read(".well-known/api-catalog", "preview"))
        self.assertEqual(catalog["linkset"][0]["anchor"], "https://preview.vvivid.dev/api/content.json")
        specification = json.loads(self.read("api/openapi.json", "preview"))
        self.assertEqual(specification["servers"], [{"url": "https://preview.vvivid.dev"}])

    def test_only_indexable_canonical_pages_are_in_production_sitemap(self):
        urls = self.sitemap()
        self.assertEqual(len(urls), len(set(urls)))
        for url in urls:
            self.assertTrue(url.startswith("https://vvivid.dev/"), url)
            page = Metadata(self.read(urlsplit(url).path.lstrip("/") + "index.html"))
            self.assertEqual([link["href"] for link in page.links if link.get("rel") == "canonical"], [url])
            self.assertNotIn("noindex", page.meta.get("robots", ""))
        for path in ("tags/golang/", "tags/json-rpc/", "tags/benchmarking/", "categories/technical-deep-dives/"):
            self.assertIn("https://vvivid.dev/" + path, urls)
            page = Metadata(self.read(path + "index.html"))
            self.assertGreater(len(page.meta["description"]), 60)
        for path in ("tags/", "categories/", "tags/http/", "tags/fixture/", "topics/"):
            self.assertNotIn("https://vvivid.dev/" + path, urls)
            self.assertEqual(Metadata(self.read(path + "index.html")).meta["robots"], "noindex, follow")
        self.assertEqual(Metadata(self.read("404.html")).meta["robots"], "noindex, follow")

    def test_preview_never_advertises_indexable_or_production_pages(self):
        self.assertEqual(self.sitemap("preview"), [])
        robots = self.read("robots.txt", "preview")
        self.assertIn("Allow: /", robots)
        self.assertNotIn("Disallow:", robots)
        self.assertNotIn("Sitemap:", robots)
        self.assertIn("Content-Signal: search=yes, ai-input=yes, ai-train=yes", robots)
        for file in self.outputs["preview"].rglob("*.html"):
            page = Metadata(file.read_text())
            self.assertEqual(page.meta["robots"], "noindex, follow", str(file))
            canonicals = [link["href"] for link in page.links if link.get("rel") == "canonical"]
            self.assertTrue(all(url.startswith("https://preview.vvivid.dev/") for url in canonicals))

    def test_author_profile_dates_and_visible_article_structure(self):
        about = Metadata(self.read("about/index.html"))
        graph = next(schema["@graph"] for schema in about.schemas if "@graph" in schema)
        person = next(entity for entity in graph if entity["@type"] == "Person")
        profile = next(entity for entity in graph if entity["@type"] == "ProfilePage")
        self.assertEqual(person["@id"], "https://vvivid.dev/about/#person")
        self.assertEqual(person["url"], "https://vvivid.dev/about/")
        self.assertEqual(profile["mainEntity"], {"@id": person["@id"]})
        self.assertEqual(about.headings.count(1), 1)
        home = Metadata(self.read("index.html"))
        self.assertEqual(home.headings.count(1), 1)
        content = json.loads(self.read("api/content.json"))
        for item in content["items"]:
            if item["section"] != "posts":
                continue
            page = Metadata(self.read(urlsplit(item["url"]).path.lstrip("/") + "index.html"))
            schema = next(schema for schema in page.schemas if schema.get("@type") == "Article")
            self.assertEqual(schema["author"], person)
            self.assertEqual(schema["publisher"], person)
            self.assertEqual(schema["datePublished"], item["published_at"])
            self.assertEqual(schema["dateModified"], item["modified_at"])
            self.assertIsNotNone(datetime.fromisoformat(schema["datePublished"]).utcoffset())
            self.assertEqual(page.meta["article:published_time"], schema["datePublished"])
            self.assertEqual(page.meta["article:modified_time"], schema["dateModified"])
            self.assertTrue(page.byline)
            self.assertEqual(page.headings[0], 1)
            self.assertEqual(page.headings.count(1), 1)
            self.assertTrue(all(b <= a + 1 for a, b in zip(page.headings, page.headings[1:])))
        fixture = Metadata(self.read("posts/fixture-published/index.html"))
        schema = next(schema for schema in fixture.schemas if schema.get("@type") == "Article")
        self.assertEqual(schema["datePublished"], "2002-01-01T00:00:00Z")
        self.assertEqual(schema["dateModified"], "2003-01-01T00:00:00Z")

    def test_math_diagrams_and_social_encoding_are_page_specific(self):
        output = self.outputs["production"]
        math_pages = []
        diagram_pages = []
        for file in output.rglob("*.html"):
            html = file.read_text()
            if "katex@" in html:
                math_pages.append(file.relative_to(output).as_posix())
            if "mermaid@" in html:
                diagram_pages.append(file.relative_to(output).as_posix())
        self.assertEqual(math_pages, ["posts/handling_fractional_cents_integration/index.html"])
        self.assertEqual(diagram_pages, ["posts/jsonrpc_example/index.html"])
        jpeg = (output / "images/site-cover.jpg").read_bytes()
        png = (output / "images/site-cover.png").read_bytes()
        self.assertTrue(jpeg.startswith(b"\xff\xd8"))
        self.assertEqual(png, (self.site / "assets/images/site-cover.png").read_bytes())
        self.assertLess(len(jpeg), len(png) / 5)
        self.assertEqual(Metadata(self.read("index.html")).meta["og:image"], "https://vvivid.dev/images/site-cover.jpg")


if __name__ == "__main__":
    unittest.main(verbosity=2)
