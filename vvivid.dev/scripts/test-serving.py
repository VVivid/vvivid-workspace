#!/usr/bin/env python3
"""Exercise the built nginx image over HTTP, without deployment credentials."""

import http.client
import json
import re
import sys
import unittest
from html.parser import HTMLParser
from urllib.parse import urlsplit


class StructuredData(HTMLParser):
    def __init__(self):
        super().__init__()
        self.in_schema = False
        self.documents = []

    def handle_starttag(self, tag, attrs):
        self.in_schema = tag == "script" and dict(attrs).get("type") == "application/ld+json"

    def handle_endtag(self, tag):
        if tag == "script":
            self.in_schema = False

    def handle_data(self, data):
        if self.in_schema:
            self.documents.append(json.loads(data))


class ServingTests(unittest.TestCase):
    origin = None

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

    def test_published_pages_and_explicit_markdown(self):
        pages = {
            "/": "Recent posts",
            "/about/": "Ou David",
            "/projects/": "Go Language Case Studies",
            "/projects/go-lang-case-study/": "strings.Builder",
            "/posts/": "What Really Happens When You Run a Shell Command?",
            "/posts/process_api_behind_every_shell_command/": "fork()",
            "/tags/": "Golang",
            "/tags/golang/": "Go Language Case Studies",
            "/categories/technical-deep-dives/": "HTTP Request Journey",
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
        graph = next(document["@graph"] for document in schema.documents if "@graph" in document)
        self.assertEqual({entity["@type"] for entity in graph}, {"Person", "WebSite"})
        self.assertEqual(next(entity["name"] for entity in graph if entity["@type"] == "Person"), "Ou David")

    def test_errors_redirects_and_assets(self):
        for path in ("/missing/", "/missing/index.md", "/AGENTS.md", "/aGeNt.md", "/nested/AGENT.md", "/AGENTS.md/index.md", "/.well-known/api-catalog", "/.well-known/oauth-authorization-server", "/.well-known/openid-configuration", "/openapi.json"):
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
    if len(sys.argv) != 2:
        sys.exit(f"Usage: {sys.argv[0]} http://127.0.0.1:PORT")
    ServingTests.origin = urlsplit(sys.argv.pop())
    if ServingTests.origin.scheme != "http" or ServingTests.origin.hostname != "127.0.0.1":
        sys.exit("Use the serving image's loopback-only HTTP origin.")
    unittest.main(verbosity=2)
