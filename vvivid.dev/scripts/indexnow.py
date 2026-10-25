#!/usr/bin/env python3
"""Prepare an IndexNow batch from indexable production output; submit only on request."""

import argparse
from html.parser import HTMLParser
import json
from pathlib import Path
import re
import sys
from urllib import request
from urllib.parse import urlsplit
import xml.etree.ElementTree as ET


PRODUCTION = "https://vvivid.dev/"
ENDPOINT = "https://api.indexnow.org/indexnow"


class PageMetadata(HTMLParser):
    def __init__(self):
        super().__init__()
        self.canonical = []
        self.noindex = False

    def handle_starttag(self, tag, attrs):
        fields = dict(attrs)
        if tag == "link" and fields.get("rel", "").lower() == "canonical":
            self.canonical.append(fields.get("href"))
        if tag == "meta" and fields.get("name", "").lower() in ("robots", "bingbot"):
            directives = re.split(r"[,\s]+", fields.get("content", "").lower())
            self.noindex |= "noindex" in directives or "none" in directives


def production_path(url):
    """Reject aliases and ambiguous paths before resolving a local artifact."""
    parsed = urlsplit(url)
    if (parsed.scheme != "https" or parsed.netloc != "vvivid.dev" or parsed.query
            or parsed.fragment or not parsed.path.startswith("/")
            or not parsed.path.endswith("/") or "%" in parsed.path
            or "\\" in parsed.path or "//" in parsed.path
            or any(part in (".", "..") for part in parsed.path.split("/"))):
        raise ValueError(f"Not a canonical production page URL: {url}")
    return parsed.path


def build_payload(public_dir, key, requested_urls=None):
    if not re.fullmatch(r"[a-zA-Z0-9-]{8,128}", key):
        raise ValueError("IndexNow key must contain 8–128 letters, digits, or hyphens")
    public_dir = Path(public_dir).resolve()
    key_file = public_dir / f"{key}.txt"
    if not key_file.is_file() or key_file.read_text().strip() != key:
        raise ValueError("Built public verification file must match the configured IndexNow key")
    root = ET.parse(public_dir / "sitemap.xml").getroot()
    sitemap_urls = [node.text for node in root.findall("{*}url/{*}loc")]
    if not sitemap_urls:
        raise ValueError("No indexable production sitemap URLs; preview builds cannot be submitted")
    eligible = {}
    for url in sitemap_urls:
        path = production_path(url)
        artifact = (public_dir / path.lstrip("/") / "index.html").resolve()
        if not artifact.is_relative_to(public_dir):
            raise ValueError("Page artifact escapes the public directory")
        metadata = PageMetadata()
        metadata.feed(artifact.read_text(encoding="utf-8"))
        if metadata.canonical != [url] or metadata.noindex:
            raise ValueError(f"Sitemap page must have one matching canonical and permit indexing: {url}")
        eligible[url] = True
    selected = list(eligible) if requested_urls is None else list(dict.fromkeys(requested_urls))
    if not selected or len(selected) > 10000:
        raise ValueError("Select between 1 and 10,000 eligible URLs per submission")
    for url in selected:
        production_path(url)
        if url not in eligible:
            raise ValueError(f"URL is absent from the indexable production sitemap: {url}")
    return {"host": "vvivid.dev", "key": key, "keyLocation": f"{PRODUCTION}{key}.txt", "urlList": selected}


def submit(payload):
    verification_url = payload["keyLocation"]
    # Verify the exact public key after deployment. A redirect to another origin
    # must not silently turn the ownership check into a different resource.
    with request.urlopen(verification_url, timeout=15) as response:
        if response.status != 200 or response.geturl() != verification_url:
            raise ValueError("Public verification file must return HTTP 200 without a redirect")
        if response.read(1024).decode("utf-8").strip() != payload["key"]:
            raise ValueError("Public verification file does not match the submission key")
    message = request.Request(ENDPOINT, data=json.dumps(payload).encode("utf-8"),
                              headers={"Content-Type": "application/json"}, method="POST")
    with request.urlopen(message, timeout=30) as response:
        if response.status not in (200, 202):
            raise ValueError(f"Unexpected IndexNow status: {response.status}")
        print(f"IndexNow accepted {len(payload['urlList'])} URLs (HTTP {response.status}); indexing is not guaranteed.")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--public-dir", type=Path, required=True, help="Fresh production build output")
    parser.add_argument("--urls-file", type=Path, help="One changed canonical URL per line; default is all eligible pages")
    parser.add_argument("--submit", action="store_true", help="Verify the deployed public key and send this batch")
    options = parser.parse_args()
    project = Path(__file__).resolve().parents[1]
    key = json.loads((project / "data/indexnow.json").read_text())["key"]
    urls = None
    if options.urls_file:
        urls = [line.strip() for line in options.urls_file.read_text().splitlines() if line.strip()]
    try:
        payload = build_payload(options.public_dir, key, urls)
        if options.submit:
            submit(payload)
        else:
            print(json.dumps(payload, indent=2))
    except (OSError, ValueError, ET.ParseError) as error:
        parser.exit(1, f"IndexNow: {error}\n")


if __name__ == "__main__":
    main()
