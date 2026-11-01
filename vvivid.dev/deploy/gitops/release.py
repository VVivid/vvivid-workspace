#!/usr/bin/env python3
"""The website's release identity and desired-state contract. No network writes."""

from dataclasses import dataclass
import json
from pathlib import Path
import re


SOURCE_REPOSITORY = "https://github.com/VVivid/vvivid-workspace.git"
GITOPS_REPOSITORY = "https://github.com/VVivid/homelab-gitops.git"
REGISTRY = "registry.lab.vvivid.dev"
IMAGE = REGISTRY + "/products/vvivid-website"
CHART = REGISTRY + "/products/charts/vvivid-website"
VERSION = r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)"
SHA = re.compile(r"[0-9a-f]{40}")
DIGEST = re.compile(r"sha256:[0-9a-f]{64}")


class ReleaseError(Exception):
    """A release failed a policy check; stop without changing desired state."""


def require(condition, message):
    if not condition:
        raise ReleaseError(message)


@dataclass(frozen=True)
class Release:
    ref: str
    sha: str
    run_id: int
    attempt: int
    channel: str
    version: str

    @classmethod
    def parse(cls, ref, sha, run_id, attempt):
        require(SHA.fullmatch(sha), "Invalid source commit")
        require(str(run_id).isdigit() and int(run_id) > 0, "Invalid workflow run ID")
        require(str(attempt).isdigit() and int(attempt) > 0, "Invalid workflow attempt")
        for prefix, channel in (("refs/heads/release/vvivid.dev/", "staging"),
                                ("refs/tags/vvivid.dev/v", "production")):
            if ref.startswith(prefix) and re.fullmatch(VERSION, ref[len(prefix):]):
                return cls(ref, sha, int(run_id), int(attempt), channel, ref[len(prefix):])
        raise ReleaseError("Only release/vvivid.dev/X.Y.Z branches and vvivid.dev/vX.Y.Z tags publish")

    @property
    def base_url(self):
        return "https://preview.vvivid.dev/" if self.channel == "staging" else "https://vvivid.dev/"

    @property
    def image_tag(self):
        return "release-" + self.version if self.channel == "staging" else self.version

    @property
    def source_tag(self):
        identity = f"{self.channel}-{self.version}-sha-{self.sha}"
        return identity + (f"-run-{self.run_id}-{self.attempt}" if self.channel == "staging" else "")

    @property
    def chart_version(self):
        return (f"{self.version}-staging.{self.run_id}.{self.attempt}"
                if self.channel == "staging" else self.version)

    @property
    def path(self):
        return Path("environments/websites/vvivid-dev") / self.channel / "fleet.yaml"

    @property
    def order(self):
        """Compare versions first, then workflow runs and retry attempts."""
        return tuple(map(int, self.version.split("."))) + (self.run_id, self.attempt)

    def provenance(self, image_digest, chart_digest):
        require(DIGEST.fullmatch(image_digest), "Invalid image digest")
        require(DIGEST.fullmatch(chart_digest), "Invalid chart digest")
        return {"channel": self.channel, "version": self.version, "sourceRef": self.ref,
                "sourceSha": self.sha, "sourceRepository": SOURCE_REPOSITORY,
                "runId": self.run_id, "runAttempt": self.attempt,
                "runUrl": f"https://github.com/VVivid/vvivid-workspace/actions/runs/{self.run_id}",
                "imageDigest": image_digest, "chartVersion": self.chart_version,
                "chartDigest": chart_digest}


def update_desired_state(document, release, image_digest, chart_digest):
    """Change only release-owned fields; refuse reuse, stale runs and substitutions."""
    provenance = release.provenance(image_digest, chart_digest)
    require(document["defaultNamespace"] == "website-" + release.channel, "Wrong target namespace")
    helm = document["helm"]
    require(helm["repo"] == "oci://" + CHART and "chart" not in helm, "Unexpected Helm chart")
    values = helm["values"]
    require(values["image"]["repository"] == IMAGE, "Unexpected image repository")
    require(values["hostname"] == release.base_url.split("/")[2], "Wrong environment hostname")
    recorded_provenance = values["provenance"]
    if recorded_provenance.get("sourceSha") != "UNCONFIGURED":
        recorded_release = Release.parse(recorded_provenance["sourceRef"], recorded_provenance["sourceSha"],
                                         recorded_provenance["runId"], recorded_provenance["runAttempt"])
        require(recorded_release.channel == release.channel, "Cross-environment state")
        require(recorded_provenance == recorded_release.provenance(
                    recorded_provenance["imageDigest"], recorded_provenance["chartDigest"]),
                "Inconsistent existing provenance")
        require(recorded_provenance["imageDigest"] == values["image"]["digest"] and
                recorded_provenance["chartVersion"] == helm["version"], "Inconsistent existing release")
        if recorded_provenance == provenance:
            return False
        require(not (recorded_release.ref == release.ref and recorded_release.sha == release.sha and
                     recorded_release.run_id == release.run_id and recorded_release.attempt == release.attempt),
                "Release digest/provenance substitution")
        if release.channel == "production":
            require(release.order[:3] > recorded_release.order[:3], "Production version reuse or downgrade")
        else:
            require(release.order > recorded_release.order, "Superseded staging release")
    helm["version"] = release.chart_version
    values["image"]["digest"] = image_digest
    values["provenance"] = provenance
    return True


def read_document(path):
    require(not path.is_symlink(), "Desired-state file must not be a symlink")
    return json.loads(path.read_text())
