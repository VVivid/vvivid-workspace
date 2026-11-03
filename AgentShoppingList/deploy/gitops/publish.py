#!/usr/bin/env python3
"""Publish checked local image/chart artifacts, then verify their exact digests."""

import argparse
import base64
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import urllib.error
import urllib.request

from promote import authenticated_environment, verify_source
from release import CHART, DIGEST, IMAGE, REGISTRY, Release, ReleaseError, require


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, _request, _file, _code, _message, _headers, _url):
        raise ReleaseError("Registry redirected a credential-bearing request")


class Registry:
    def __init__(self, username, password):
        require(username and password, "Registry publication credentials are required")
        self.authorization = "Basic " + base64.b64encode(f"{username}:{password}".encode()).decode()
        self.opener = urllib.request.build_opener(NoRedirect)

    def digest(self, repository, tag):
        require(repository in (IMAGE, CHART), "Unexpected registry repository")
        path = repository.removeprefix(REGISTRY + "/")
        request = urllib.request.Request(f"https://{REGISTRY}/v2/{path}/manifests/{tag}", method="HEAD",
                                         headers={"Authorization": self.authorization, "Accept":
                                                  "application/vnd.oci.image.manifest.v1+json, "
                                                  "application/vnd.oci.image.index.v1+json, "
                                                  "application/vnd.docker.distribution.manifest.list.v2+json, "
                                                  "application/vnd.docker.distribution.manifest.v2+json"})
        try:
            with self.opener.open(request, timeout=30) as response:
                digest = response.headers.get("Docker-Content-Digest", "")
                require(DIGEST.fullmatch(digest), "Registry returned an invalid manifest digest")
                return digest
        except urllib.error.HTTPError as error:
            if error.code == 404:
                return None
            raise ReleaseError(f"Registry verification failed (HTTP {error.code})") from None
        except urllib.error.URLError:
            raise ReleaseError("Registry verification failed (connection/TLS)") from None


def require_unused(registry, release):
    require(registry.digest(IMAGE, release.source_tag) is None, "Source image tag already exists")
    require(registry.digest(CHART, release.chart_version) is None, "Chart version already exists")
    if release.channel == "production":
        require(registry.digest(IMAGE, release.image_tag) is None, "Production version already exists")


def run(*arguments, include_stderr=False):
    result = subprocess.run(arguments, text=True, capture_output=True)
    if result.returncode:
        raise ReleaseError("Artifact command failed: " + arguments[0] + " " + arguments[1])
    return result.stdout + result.stderr if include_stderr else result.stdout


def publish(image, release, registry, verify, output):
    """Recheck the source ref before uploads; partial publication stays for audit."""
    verify()
    require_unused(registry, release)
    inspection = json.loads(run("docker", "image", "inspect", image))[0]
    require(inspection["Os"] == "linux" and inspection["Architecture"] == "amd64", "Image must be Linux amd64")
    labels = inspection["Config"].get("Labels", {})
    require(labels.get("org.opencontainers.image.revision") == release.sha and
            labels.get("dev.vvivid.source-ref") == release.ref and
            labels.get("dev.vvivid.base-url") == release.base_url, "Image provenance does not match release")
    source_image = IMAGE + ":" + release.source_tag
    run("docker", "tag", image, source_image)
    verify()
    run("docker", "push", source_image)
    image_digest = registry.digest(IMAGE, release.source_tag)
    require(image_digest is not None, "Published image is missing")
    repo_digests = json.loads(run("docker", "image", "inspect", source_image))[0]["RepoDigests"]
    require(IMAGE + "@" + image_digest in repo_digests, "Published image digest substitution")
    chart_source = Path(__file__).resolve().parents[2] / "chart"
    with tempfile.TemporaryDirectory(prefix="shopping-chart-") as directory:
        run("helm", "package", str(chart_source), "--version", release.chart_version,
            "--app-version", release.sha, "--destination", directory)
        verify()
        require(registry.digest(CHART, release.chart_version) is None, "Chart version appeared during publication")
        package = str(Path(directory) / f"agent-shopping-list-{release.chart_version}.tgz")
        push_output = run("helm", "push", package, "oci://" + REGISTRY + "/products/charts", include_stderr=True)
        pushed_digest = re.search(r"^Digest: (sha256:[0-9a-f]{64})$", push_output, re.MULTILINE)
        require(pushed_digest, "Helm did not report the pushed chart digest")
    chart_digest = registry.digest(CHART, release.chart_version)
    require(chart_digest == pushed_digest[1], "Published chart digest substitution")
    verify()
    if release.channel == "production":
        require(registry.digest(IMAGE, release.image_tag) is None, "Production version appeared during publication")
    alias = IMAGE + ":" + release.image_tag
    run("docker", "tag", source_image, alias)
    run("docker", "push", alias)
    require(registry.digest(IMAGE, release.source_tag) == image_digest and
            registry.digest(IMAGE, release.image_tag) == image_digest and
            registry.digest(CHART, release.chart_version) == chart_digest, "Artifact digest substitution")
    output.write_text(json.dumps(release.provenance(image_digest, chart_digest), indent=2) + "\n")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--plan", action="store_true", help="Print validated release fields without publishing")
    parser.add_argument("--image", default="agentshoppinglist:release-check")
    parser.add_argument("--output", type=Path, default=Path("release.json"))
    arguments = parser.parse_args()
    release = Release.parse(os.environ["GITHUB_REF"], os.environ["GITHUB_SHA"],
                            os.environ["GITHUB_RUN_ID"], os.environ["GITHUB_RUN_ATTEMPT"])
    if arguments.plan:
        print(json.dumps({"base_url": release.base_url, "chart_version": release.chart_version,
                          "image_tag": release.image_tag, "source_tag": release.source_tag,
                          "channel": release.channel}))
        return
    registry = Registry(os.environ.get("ZOT_USERNAME"), os.environ.get("ZOT_PASSWORD"))
    require(os.environ.get("SOURCE_TOKEN"), "Source read token is required")
    with tempfile.TemporaryDirectory(prefix="shopping-source-auth-") as directory:
        environment = authenticated_environment(directory, os.environ["SOURCE_TOKEN"])
        publish(arguments.image, release, registry, lambda: verify_source(release, environment), arguments.output)


if __name__ == "__main__":
    try:
        main()
    except (ReleaseError, KeyError, ValueError) as error:
        raise SystemExit(str(error)) from None
