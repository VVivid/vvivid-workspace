#!/usr/bin/env python3
"""IndexNow safeguards use only temporary files and fake HTTP responses."""

from contextlib import redirect_stdout
from io import BytesIO, StringIO
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import indexnow


class Response(BytesIO):
    def __init__(self, body=b"", status=200, url=""):
        super().__init__(body)
        self.status = status
        self.url = url

    def geturl(self):
        return self.url


class IndexNowTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.public = Path(self.temporary.name)
        project = Path(__file__).resolve().parents[1]
        self.key = json.loads((project / "data/indexnow.json").read_text())["key"]
        (self.public / f"{self.key}.txt").write_text(self.key + "\n")
        self.url = "https://vvivid.dev/posts/example/"
        self.write_page()
        self.write_sitemap([self.url])

    def write_page(self, canonical=None, robots=""):
        path = self.public / "posts/example/index.html"
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(f'<link rel="canonical" href="{canonical or self.url}">'
                        f'<meta name="robots" content="{robots}">')

    def write_sitemap(self, urls):
        (self.public / "sitemap.xml").write_text(
            '<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">' +
            ''.join(f'<url><loc>{url}</loc></url>' for url in urls) + '</urlset>')

    def test_default_is_a_reviewable_dry_run(self):
        output = StringIO()
        with patch("sys.argv", ["indexnow.py", "--public-dir", str(self.public)]), \
                patch.object(indexnow, "submit") as submit, redirect_stdout(output):
            indexnow.main()
        submit.assert_not_called()
        self.assertEqual(json.loads(output.getvalue()), {
            "host": "vvivid.dev", "key": self.key,
            "keyLocation": f"https://vvivid.dev/{self.key}.txt", "urlList": [self.url]})

    def test_key_must_match_public_artifact(self):
        for key in ("bad", "../not-a-key", "b" * 32):
            with self.subTest(key=key), self.assertRaises(ValueError):
                indexnow.build_payload(self.public, key)
        (self.public / f"{self.key}.txt").write_text("different-key")
        with self.assertRaises(ValueError):
            indexnow.build_payload(self.public, self.key)

    def test_preview_and_noncanonical_hosts_are_rejected(self):
        for url in ("https://preview.vvivid.dev/posts/example/", "https://www.vvivid.dev/posts/example/",
                    "http://vvivid.dev/posts/example/", "https://vvivid.dev:443/posts/example/",
                    "https://vvivid.dev/posts/example/?preview=true", "https://vvivid.dev/posts/example/#intro",
                    "https://vvivid.dev/a/../posts/example/", "https://vvivid.dev/%2e%2e/", "https://vvivid.dev//posts/example/"):
            with self.subTest(url=url), self.assertRaises(ValueError):
                self.write_sitemap([url])
                indexnow.build_payload(self.public, self.key)
        self.write_sitemap([])
        with self.assertRaisesRegex(ValueError, "preview"):
            indexnow.build_payload(self.public, self.key)

    def test_noindex_or_wrong_canonical_cannot_be_submitted(self):
        for canonical, robots in ((self.url, "noindex, follow"), (self.url, "none"),
                                  ("https://preview.vvivid.dev/posts/example/", "")):
            with self.subTest(canonical=canonical, robots=robots), self.assertRaises(ValueError):
                self.write_page(canonical, robots)
                indexnow.build_payload(self.public, self.key)

    def test_selection_is_limited_to_eligible_sitemap_pages(self):
        result = indexnow.build_payload(self.public, self.key, [self.url, self.url])
        self.assertEqual(result["urlList"], [self.url])
        for selection in ([], ["https://vvivid.dev/tags/thin/"], ["https://preview.vvivid.dev/"]):
            with self.subTest(selection=selection), self.assertRaises(ValueError):
                indexnow.build_payload(self.public, self.key, selection)

    def test_submit_checks_deployed_key_before_posting(self):
        payload = indexnow.build_payload(self.public, self.key)
        with patch.object(indexnow.request, "urlopen", side_effect=[
                Response(self.key.encode(), url=payload["keyLocation"]), Response(status=202)]) as http, \
                redirect_stdout(StringIO()):
            indexnow.submit(payload)
        self.assertEqual(http.call_count, 2)
        sent = http.call_args_list[1].args[0]
        self.assertEqual(sent.full_url, "https://api.indexnow.org/indexnow")
        self.assertEqual(sent.method, "POST")
        self.assertEqual(json.loads(sent.data), payload)
        for body, url in ((b"wrong-key", payload["keyLocation"]),
                          (self.key.encode(), "https://preview.vvivid.dev/key.txt")):
            with self.subTest(body=body, url=url), \
                    patch.object(indexnow.request, "urlopen", return_value=Response(body, url=url)) as http, \
                    self.assertRaises(ValueError):
                indexnow.submit(payload)
            self.assertEqual(http.call_count, 1)


if __name__ == "__main__":
    unittest.main(verbosity=2)
