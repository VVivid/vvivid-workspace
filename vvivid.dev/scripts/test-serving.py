#!/usr/bin/env python3
"""Exercise the built nginx image over HTTP, without deployment credentials."""

from datetime import datetime
import hashlib
import http.client
import json
from pathlib import Path
import re
import sys
import unittest
from html.parser import HTMLParser
from urllib.parse import urlsplit
import xml.etree.ElementTree as ET


class StructuredData(HTMLParser):
    def __init__(self):
        super().__init__()
        self.in_schema = False
        self.documents = []
        self.links = []
        self.meta = {}
        self.h1_count = 0
        self.byline = False

    def handle_starttag(self, tag, attrs):
        self.in_schema = tag == "script" and dict(attrs).get("type") == "application/ld+json"
        if tag == "link":
            self.links.append(dict(attrs))
        fields = dict(attrs)
        if tag == "meta":
            self.meta[fields.get("name", fields.get("property"))] = fields.get("content")
        if tag == "h1":
            self.h1_count += 1
        if tag == "a" and fields.get("rel") == "author":
            self.byline = fields.get("href") == "/about/"

    def handle_endtag(self, tag):
        if tag == "script":
            self.in_schema = False

    def handle_data(self, data):
        if self.in_schema:
            self.documents.append(json.loads(data))


class ServingTests(unittest.TestCase):
    origin = None
    expected_base_url = None

    def request(self, path, accept=None, method="GET", headers=None):
        request_headers = dict(headers or {})
        if accept is not None:
            request_headers["Accept"] = accept
        connection = http.client.HTTPConnection(self.origin.hostname, self.origin.port, timeout=5)
        try:
            connection.request(method, path, headers=request_headers)
            response = connection.getresponse()
            body = response.read()
            # Preserve repeated Vary and Cache-Control fields for assertions.
            fields = {}
            for name, value in response.getheaders():
                key = name.lower()
                fields[key] = fields[key] + ", " + value if key in fields else value
            return response.status, fields, body
        finally:
            connection.close()

    def assert_private_cache(self, headers):
        self.assertIn("no-store", headers.get("cache-control", ""))
        self.assertIn("accept", headers.get("vary", "").lower().split(", "))
        self.assertEqual(headers.get("x-content-type-options"), "nosniff")

    def test_build_base_url_and_preview_indexing(self):
        snapshot = json.loads(self.request("/api/content.json")[2])
        base = snapshot["site"]["url"]
        if self.expected_base_url is not None:
            self.assertEqual(base, self.expected_base_url)
        paths = {"/", "/about/", "/api/"} | {urlsplit(item["url"]).path for item in snapshot["items"]}
        for path in paths:
            with self.subTest(path=path):
                status, headers, html = self.request(path, headers={"Host": urlsplit(base).hostname})
                self.assertEqual(status, 200)
                page = StructuredData()
                page.feed(html.decode())
                self.assertEqual([link["href"] for link in page.links if link.get("rel") == "canonical"],
                                 [base.rstrip("/") + path])
                if base != "https://vvivid.dev/":
                    self.assertEqual(page.meta["robots"], "noindex, follow")
                    self.assertEqual(headers["x-robots-tag"], "noindex, follow")

    def test_published_pages_and_explicit_markdown(self):
        pages = {
            "/": "Recent posts",
            "/about/": "Ou David",
            "/projects/": "Go Language Case Studies",
            "/projects/go-lang-case-study/": "strings.Builder",
            "/posts/": "What Really Happens When You Run a Shell Command?",
            "/posts/process_api_behind_every_shell_command/": "fork()",
            "/tags/": "Go backend engineering",
            "/tags/golang/": "Go Language Case Studies",
            "/categories/technical-deep-dives/": "HTTP Request Journey",
            "/api/": "GET /api/content.json",
        }
        for path, expected in pages.items():
            with self.subTest(path=path):
                html_status, html_headers, html = self.request(path)
                status, headers, markdown = self.request(path, "text/markdown")
                self.assertEqual((html_status, status), (200, 200))
                self.assertIn("text/html", html_headers["content-type"])
                self.assertIn("text/markdown", headers["content-type"])
                self.assertIn("charset=utf-8", headers["content-type"])
                self.assertTrue(html.lower().startswith(b"<!doctype html>"))
                self.assertTrue(markdown.startswith(b"# "))
                self.assertNotIn(b"<html", markdown)
                self.assertIn(expected, markdown.decode())
                self.assert_private_cache(html_headers)
                self.assert_private_cache(headers)
                explicit = self.request(path + "index.md")
                self.assertEqual((explicit[0], explicit[2]), (200, markdown))
                self.assertIn("text/markdown", explicit[1]["content-type"])
                self.assert_private_cache(explicit[1])
                # Direct HTML URLs participate in the same negotiation.
                direct = self.request(path + "index.html", "text/markdown")
                self.assertEqual((direct[0], direct[2]), (200, markdown))

    def test_accept_preferences_and_exclusions(self):
        cases = [
            (None, "text/html"),
            ("*/*", "text/html"),
            ("text/*", "text/html"),
            ("text/markdown", "text/markdown"),
            ("text/markdown;q=0, text/html", "text/html"),
            ("text/markdown;q=0.7, text/html;q=0.8", "text/html"),
            ("text/html;q=0.3, text/markdown;q=0.8", "text/markdown"),
            ("text/markdown;q=0.8, text/html;q=0.8", "text/html"),
            ("TeXt/MaRkDoWn ; Q=0.9, text/html;q=0.1", "text/markdown"),
            ("text/markdown;q=0, text/*;q=1", "text/html"),
            ("text/html;q=0, text/*;q=0.8", "text/markdown"),
            ("text/markdown;charset=utf-8", "text/markdown"),
            ("text/markdown;charset=iso-8859-1, text/html", "text/html"),
            ("text/markdown;q=invalid, text/html", "text/html"),
            ("text/html;q=0, text/*;charset=utf-8;q=0.8", "text/markdown"),
        ]
        for accept, expected in cases:
            with self.subTest(accept=accept):
                status, headers, _ = self.request("/about/", accept)
                self.assertEqual(status, 200)
                self.assertIn(expected, headers["content-type"])
        for accept in ("text/markdown;q=0", "text/html;q=0, text/markdown;q=0", "application/json"):
            with self.subTest(accept=accept):
                status, headers, _ = self.request("/about/", accept)
                self.assertEqual(status, 406)
                self.assert_private_cache(headers)

    def test_head_queries_and_conditional_requests(self):
        for accept in ("text/html", "text/markdown"):
            with self.subTest(accept=accept):
                status, headers, body = self.request("/about/?source=agent", accept)
                self.assertEqual(status, 200)
                head_status, head_headers, head_body = self.request("/about/?source=agent", accept, method="HEAD")
                self.assertEqual((head_status, head_body), (200, b""))
                self.assertEqual(head_headers["content-type"], headers["content-type"])
                self.assertEqual(int(head_headers["content-length"]), len(body))
                self.assertNotIn("etag", headers)
                conditional = {"If-Modified-Since": headers["last-modified"], "If-None-Match": '"previous-format"'}
                self.assertEqual(self.request("/about/", accept, headers=conditional)[0], 200)
                # Last-Modified timestamps are shared across build outputs.
                self.assertEqual(self.request("/about/", accept, headers={"If-Modified-Since": headers["last-modified"]})[0], 200)
        for accept in ("text/markdown", "text/html", "text/markdown", "text/html"):
            status, headers, _ = self.request("/", accept)
            self.assertEqual(status, 200)
            self.assertIn(accept, headers["content-type"])
            self.assert_private_cache(headers)

    def test_discovery_links_and_structured_data(self):
        _, headers, html = self.request("/")
        links = re.findall(r'<([^>]+)>; rel="([^"]+)"(?:; type="([^"]+)")?', headers["link"])
        self.assertIn(("/index.md", "alternate", "text/markdown"), links)
        self.assertIn(("/auth.md", "describedby", "text/markdown"), links)
        self.assertIn(("/.well-known/api-catalog", "api-catalog", "application/linkset+json"), links)
        self.assertIn(("/api/openapi.json", "service-desc", "application/json"), links)
        self.assertIn(("/api/", "service-doc", "text/html"), links)
        for path, _, media_type in links:
            with self.subTest(link=path):
                status, linked_headers, _ = self.request(path)
                self.assertEqual(status, 200)
                if media_type:
                    self.assertIn(media_type, linked_headers["content-type"])
        status, _, index = self.request("/llms.txt")
        self.assertEqual(status, 200)
        self.assertIn(b"process_api_behind_every_shell_command/index.md", index)
        # Every same-site content link in the generated index resolves locally.
        for url in re.findall(r"\]\((https?://[^)]+)\)", index.decode()):
            path = urlsplit(url).path
            self.assertEqual(self.request(path)[0], 200, path)
        status, headers, auth = self.request("/auth.md")
        self.assertEqual(status, 200)
        self.assertIn("text/markdown", headers["content-type"])
        self.assertTrue(auth.startswith(b"# auth.md "))
        self.assertIn(b"No account, registration", auth)
        self.assertIn(b"not applicable", auth)
        schema = StructuredData()
        schema.feed(html.decode())
        for relation, path in (("api-catalog", "/.well-known/api-catalog"),
                               ("service-desc", "/api/openapi.json"), ("service-doc", "/api/")):
            self.assertTrue(any(link.get("rel") == relation and urlsplit(link.get("href", "")).path == path
                                for link in schema.links), relation)
        graph = next(document["@graph"] for document in schema.documents if "@graph" in document)
        self.assertEqual({entity["@type"] for entity in graph}, {"Person", "WebSite"})
        self.assertEqual(next(entity["name"] for entity in graph if entity["@type"] == "Person"), "Ou David")

    def test_content_api_and_openapi_contract(self):
        status, headers, body = self.request("/api/content.json", "application/json")
        self.assertEqual(status, 200)
        self.assertIn("application/json", headers["content-type"])
        self.assert_private_cache(headers)
        snapshot = json.loads(body)
        self.assertEqual(snapshot["version"], "1")
        self.assertEqual(snapshot["count"], len(snapshot["items"]))
        self.assertGreater(snapshot["count"], 0)
        self.assertEqual(self.request("/api/content.json?section=missing&limit=0")[2], body)
        # These fixed JSON resources are independent of page Accept negotiation.
        self.assertEqual(self.request("/api/content.json", "text/markdown")[2], body)
        head_status, head_headers, head_body = self.request("/api/content.json", method="HEAD")
        self.assertEqual((head_status, head_body), (200, b""))
        self.assertEqual(head_headers["content-type"], headers["content-type"])
        self.assertEqual(int(head_headers["content-length"]), len(body))
        self.assertEqual(self.request("/api/content.json", method="POST")[0], 405)

        status, spec_headers, spec_body = self.request("/api/openapi.json")
        self.assertEqual(status, 200)
        self.assertIn("application/json", spec_headers["content-type"])
        self.assert_private_cache(spec_headers)
        specification = json.loads(spec_body)
        self.assertEqual(specification["openapi"], "3.1.1")
        self.assertEqual(specification["security"], [])
        self.assertEqual(specification["servers"][0]["url"].rstrip("/"), snapshot["site"]["url"].rstrip("/"))
        operation = specification["paths"]["/api/content.json"]
        self.assertEqual(set(operation), {"get", "head"})
        reference = operation["get"]["responses"]["200"]["content"]["application/json"]["schema"]["$ref"]
        self.assertEqual(reference, "#/components/schemas/ContentIndex")
        schemas = specification["components"]["schemas"]
        self.assertTrue(set(schemas["ContentIndex"]["required"]).issubset(snapshot))
        item_schema = schemas["ContentItem"]
        type_classes = {"string": str, "integer": int, "array": list}
        urls = set()
        for item in snapshot["items"]:
            with self.subTest(item=item["url"]):
                self.assertTrue(set(item_schema["required"]).issubset(item))
                for field, value in item.items():
                    definition = item_schema["properties"][field]
                    self.assertIs(type(value), type_classes[definition["type"]], field)
                    if definition["type"] == "array":
                        self.assertTrue(all(isinstance(label, str) for label in value), field)
                    if definition.get("format") == "date-time":
                        self.assertIsNotNone(datetime.fromisoformat(value).utcoffset())
                self.assertNotIn(item["url"], urls)
                urls.add(item["url"])
                for field, media_type in (("url", "text/html"), ("markdown_url", "text/markdown")):
                    resource = urlsplit(item[field])
                    self.assertEqual(resource.netloc, urlsplit(snapshot["site"]["url"]).netloc)
                    linked_status, linked_headers, linked_body = self.request(resource.path)
                    self.assertEqual(linked_status, 200)
                    self.assertIn(media_type, linked_headers["content-type"])
                    self.assertIn(item["title"].encode(), linked_body)
        self.assertTrue(any(item["section"] == "posts" for item in snapshot["items"]))
        self.assertTrue(any(item["section"] == "projects" and item.get("repository_url") for item in snapshot["items"]))
        about = next(item for item in snapshot["items"] if urlsplit(item["url"]).path == "/about/")
        self.assertNotIn("published_at", about)
        self.assertEqual(about["tags"], [])
        docs_status, docs_headers, docs_body = self.request("/api/")
        self.assertEqual(docs_status, 200)
        self.assertIn("text/html", docs_headers["content-type"])
        self.assertIn(b"curl", docs_body)
        self.assertIn(b"Unknown paths", docs_body)

    def test_api_catalog_and_public_skill(self):
        catalog_path = "/.well-known/api-catalog"
        status, headers, body = self.request(catalog_path, "application/linkset+json")
        self.assertEqual(status, 200)
        self.assertIn("application/linkset+json", headers["content-type"])
        self.assertIn('profile="https://www.rfc-editor.org/info/rfc9727"', headers["content-type"])
        self.assert_private_cache(headers)
        catalog = json.loads(body)
        self.assertGreater(len(catalog["linkset"]), 0)
        for service in catalog["linkset"]:
            self.assertEqual(urlsplit(service["anchor"]).path, "/api/content.json")
            self.assertEqual(self.request(urlsplit(service["anchor"]).path)[0], 200)
            for relation, path in (("service-desc", "/api/openapi.json"), ("service-doc", "/api/")):
                self.assertGreater(len(service[relation]), 0)
                for link in service[relation]:
                    self.assertEqual(urlsplit(link["href"]).path, path)
                    linked = self.request(path)
                    self.assertEqual(linked[0], 200)
                    self.assertIn(link["type"], linked[1]["content-type"])
        head = self.request(catalog_path, method="HEAD")
        self.assertEqual((head[0], head[2]), (200, b""))
        self.assertEqual(int(head[1]["content-length"]), len(body))

        index_path = "/.well-known/agent-skills/index.json"
        status, headers, body = self.request(index_path)
        self.assertEqual(status, 200)
        self.assertIn("application/json", headers["content-type"])
        self.assert_private_cache(headers)
        index = json.loads(body)
        self.assertEqual(index["$schema"], "https://schemas.agentskills.io/discovery/0.2.0/schema.json")
        self.assertGreater(len(index["skills"]), 0)
        for skill in index["skills"]:
            self.assertTrue({"name", "type", "description", "url", "digest"}.issubset(skill))
            self.assertRegex(skill["name"], r"^[a-z0-9]+(?:-[a-z0-9]+)*$")
            self.assertLessEqual(len(skill["name"]), 64)
            self.assertTrue(0 < len(skill["description"]) <= 1024)
            self.assertEqual(skill["type"], "skill-md")
            self.assertRegex(skill["digest"], r"^sha256:[0-9a-f]{64}$")
            path = urlsplit(skill["url"]).path
            status, headers, artifact = self.request(path)
            self.assertEqual(status, 200)
            self.assertIn("text/markdown", headers["content-type"])
            self.assert_private_cache(headers)
            self.assertEqual(skill["digest"], "sha256:" + hashlib.sha256(artifact).hexdigest())
            self.assertTrue(artifact.startswith(f'---\nname: {skill["name"]}\n'.encode()))
            self.assertIn(f'description: {skill["description"]}\n'.encode(), artifact)
            self.assertIn(b"Cite the specific article", artifact)
            self.assertIn(b"Do not invent a page", artifact)
            head = self.request(path, method="HEAD")
            self.assertEqual((head[0], head[2]), (200, b""))
            self.assertEqual(int(head[1]["content-length"]), len(artifact))
        self.assertEqual(self.request(index_path, method="HEAD")[0], 200)

    def test_content_signals(self):
        status, headers, robots = self.request("/robots.txt")
        self.assertEqual(status, 200)
        self.assertIn("text/plain", headers["content-type"])
        lines = robots.decode().splitlines()
        self.assertIn("User-agent: *", lines)
        self.assertIn("Allow: /", lines)
        self.assertIn("Content-Signal: search=yes, ai-input=yes, ai-train=yes", lines)
        self.assertEqual(len([line for line in lines if line.startswith("Content-Signal:")]), 1)
        site = json.loads(self.request("/api/content.json")[2])["site"]["url"]
        self.assertEqual(any(line.startswith("Sitemap:") for line in lines), site == "https://vvivid.dev/")

    def test_runtime_preview_noindex_and_www_redirect(self):
        for path in ("/", "/about/", "/about/index.md", "/robots.txt", "/api/content.json", "/missing/", "/images/site-cover.jpg"):
            for host in ("preview.vvivid.dev", "preview.example", "unconfigured.example"):
                with self.subTest(path=path, host=host):
                    _, headers, _ = self.request(path, headers={"Host": host})
                    self.assertEqual(headers["x-robots-tag"], "noindex, follow")
            self.assertNotIn("x-robots-tag", self.request(path, headers={"Host": "vvivid.dev"})[1])
        for path in ("/", "/posts/http_in_golang/?source=www&x=1", "/a%20b/?q=a%2Bb", "/about?source=www"):
            for method in ("GET", "HEAD"):
                with self.subTest(path=path, method=method):
                    status, headers, _ = self.request(path, method=method, headers={"Host": "www.vvivid.dev"})
                    self.assertEqual(status, 301)
                    self.assertEqual(headers["location"], "https://vvivid.dev" + path)

    def test_sitemap_and_page_indexability(self):
        snapshot = json.loads(self.request("/api/content.json")[2])
        production = snapshot["site"]["url"] == "https://vvivid.dev/"
        status, headers, body = self.request("/sitemap.xml")
        self.assertEqual(status, 200)
        self.assertIn("application/xml", headers["content-type"])
        urls = [node.text for node in ET.fromstring(body).findall("{*}url/{*}loc")]
        self.assertEqual(len(urls), len(set(urls)))
        self.assertEqual(bool(urls), production)
        for url in urls:
            with self.subTest(url=url):
                self.assertEqual(urlsplit(url).netloc, "vvivid.dev")
                status, _, html = self.request(urlsplit(url).path)
                self.assertEqual(status, 200)
                metadata = StructuredData()
                metadata.feed(html.decode())
                self.assertNotIn("noindex", metadata.meta.get("robots", ""))
                self.assertEqual([link["href"] for link in metadata.links if link.get("rel") == "canonical"], [url])
        for path in ("/tags/", "/categories/", "/tags/http/", "/topics/"):
            metadata = StructuredData()
            metadata.feed(self.request(path)[2].decode())
            self.assertEqual(metadata.meta["robots"], "noindex, follow")
            self.assertNotIn("https://vvivid.dev" + path, urls)
        for path in ("/tags/golang/", "/tags/json-rpc/", "/tags/benchmarking/", "/categories/technical-deep-dives/"):
            self.assertEqual("https://vvivid.dev" + path in urls, production)

    def test_article_authorship_dates_math_and_social_assets(self):
        snapshot = json.loads(self.request("/api/content.json")[2])
        base = snapshot["site"]["url"]
        profile = StructuredData()
        profile.feed(self.request("/about/")[2].decode())
        graph = next(document["@graph"] for document in profile.documents if "@graph" in document)
        author = next(entity for entity in graph if entity["@type"] == "Person")
        self.assertEqual(author["@id"], base + "about/#person")
        self.assertTrue(any(entity["@type"] == "ProfilePage" and entity["mainEntity"]["@id"] == author["@id"] for entity in graph))
        for item in snapshot["items"]:
            if item["section"] != "posts":
                continue
            path = urlsplit(item["url"]).path
            html = self.request(path)[2].decode()
            page = StructuredData()
            page.feed(html)
            article = next(schema for schema in page.documents if schema.get("@type") == "Article")
            self.assertEqual(article["author"], author)
            self.assertEqual(article["publisher"], author)
            self.assertEqual(article["datePublished"], item["published_at"])
            self.assertEqual(article["dateModified"], item["modified_at"])
            self.assertTrue(page.byline)
            self.assertEqual(page.h1_count, 1)
            self.assertEqual("katex@" in html, path == "/posts/handling_fractional_cents_integration/")
            self.assertEqual("mermaid@" in html, path == "/posts/jsonrpc_example/")
            self.assertEqual(self.request(urlsplit(article["image"]).path)[0], 200)
            self.assertEqual("noindex" in page.meta.get("robots", ""), base != "https://vvivid.dev/")
        homepage = StructuredData()
        homepage.feed(self.request("/")[2].decode())
        self.assertEqual(homepage.h1_count, 1)
        self.assertEqual(homepage.meta["og:image"], base + "images/site-cover.jpg")
        jpeg = self.request("/images/site-cover.jpg")
        png = self.request("/images/site-cover.png")
        self.assertEqual((jpeg[0], png[0]), (200, 200))
        self.assertIn("image/jpeg", jpeg[1]["content-type"])
        self.assertLess(len(jpeg[2]), len(png[2]) / 5)
        key = json.loads((Path(__file__).resolve().parents[1] / "data/indexnow.json").read_text())["key"]
        status, _, verification = self.request(f"/{key}.txt")
        self.assertEqual(status, 200)
        self.assertEqual(verification.decode().strip(), key)

    def test_errors_redirects_and_assets(self):
        for path in ("/missing/", "/missing/index.md", "/AGENTS.md", "/aGeNt.md", "/nested/AGENT.md", "/AGENTS.md/index.md", "/.well-known/agent-skills/AGENTS.md", "/api/missing.json", "/.well-known/agent-skills/missing/SKILL.md", "/.well-known/oauth-authorization-server", "/.well-known/openid-configuration", "/openapi.json"):
            for accept in ("text/markdown", "application/json"):
                with self.subTest(path=path, accept=accept):
                    status, headers, _ = self.request(path, accept)
                    self.assertEqual(status, 404)
                    self.assertIn("text/html", headers["content-type"])
                    self.assert_private_cache(headers)
                    self.assertNotIn("link", headers)
        status, headers, _ = self.request("/about?source=agent", "text/markdown")
        self.assertEqual(status, 301)
        self.assertEqual(urlsplit(headers["location"]).path, "/about/")
        self.assertEqual(urlsplit(headers["location"]).query, "source=agent")
        status, headers, body = self.request("/css/mobile-optimizations.css", "text/markdown")
        self.assertEqual(status, 200)
        self.assertIn("text/css", headers["content-type"])
        self.assertIn("max-age=604800", headers["cache-control"])
        self.assertEqual(headers["x-content-type-options"], "nosniff")
        self.assertGreater(len(body), 0)
        self.assertEqual(self.request("/about/", method="POST")[0], 405)


if __name__ == "__main__":
    if len(sys.argv) not in (2, 3):
        sys.exit(f"Usage: {sys.argv[0]} http://127.0.0.1:PORT [expected-base-url]")
    if len(sys.argv) == 3:
        ServingTests.expected_base_url = sys.argv.pop()
    ServingTests.origin = urlsplit(sys.argv.pop())
    if ServingTests.origin.scheme != "http" or ServingTests.origin.hostname != "127.0.0.1":
        sys.exit("Use the serving image's loopback-only HTTP origin.")
    unittest.main(verbosity=2)
