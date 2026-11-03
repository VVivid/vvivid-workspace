#!/usr/bin/env python3
"""Production-only AgentShoppingList release identity and fixed desired-state contract."""

from dataclasses import dataclass
import json
from pathlib import Path
import re

SOURCE_REPOSITORY = "https://github.com/VVivid/vvivid-workspace.git"
GITOPS_REPOSITORY = "https://github.com/VVivid/homelab-gitops.git"
REGISTRY = "registry.lab.vvivid.dev"
IMAGE = REGISTRY + "/products/agent-shopping-list"
CHART = REGISTRY + "/products/charts/agent-shopping-list"
VERSION = r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)"
SHA = re.compile(r"[0-9a-f]{40}")
DIGEST = re.compile(r"sha256:[0-9a-f]{64}")


class ReleaseError(Exception):
    """Stop before further publication when a release fails policy checks."""


def require(condition, message):
    if not condition:
        raise ReleaseError(message)


@dataclass(frozen=True)
class Release:
    ref: str
    sha: str
    run_id: int
    attempt: int
    version: str

    @classmethod
    def parse(cls, ref, sha, run_id, attempt):
        require(SHA.fullmatch(sha), "Invalid source commit")
        require(str(run_id).isdigit() and int(run_id) > 0, "Invalid workflow run ID")
        require(str(attempt).isdigit() and int(attempt) > 0, "Invalid workflow attempt")
        prefix = "refs/tags/AgentShoppingList/v"
        require(ref.startswith(prefix) and re.fullmatch(VERSION, ref[len(prefix):]),
                "Only canonical AgentShoppingList/vX.Y.Z tags publish")
        return cls(ref, sha, int(run_id), int(attempt), ref[len(prefix):])

    @property
    def channel(self):
        return "production"

    @property
    def base_url(self):
        return "https://shopping.vvivid.dev/"

    @property
    def image_tag(self):
        return self.version

    @property
    def source_tag(self):
        return f"production-{self.version}-sha-{self.sha}"

    @property
    def chart_version(self):
        return self.version

    @property
    def path(self):
        return Path("environments/rd/agent-shopping-list/production/fleet.yaml")

    @property
    def order(self):
        return tuple(map(int, self.version.split(".")))

    def provenance(self, image_digest, chart_digest):
        require(DIGEST.fullmatch(image_digest), "Invalid image digest")
        require(DIGEST.fullmatch(chart_digest), "Invalid chart digest")
        return {
            "channel": self.channel,
            "version": self.version,
            "sourceRef": self.ref,
            "sourceSha": self.sha,
            "sourceRepository": SOURCE_REPOSITORY,
            "runId": self.run_id,
            "runAttempt": self.attempt,
            "runUrl": f"https://github.com/VVivid/vvivid-workspace/actions/runs/{self.run_id}",
            "imageDigest": image_digest,
            "chartVersion": self.chart_version,
            "chartDigest": chart_digest,
        }


def update_desired_state(document, release, image_digest, chart_digest):
    """Change exactly version, image digest and provenance; preserve operator values."""
    provenance = release.provenance(image_digest, chart_digest)
    require(document["defaultNamespace"] == "vvivid-rd", "Wrong target namespace")
    require(document.get("deleteNamespace") is False and document.get("keepResources") is True,
            "Namespace/resource retention policy drift")
    helm = document["helm"]
    require(helm["repo"] == "oci://" + CHART and "chart" not in helm and
            helm["releaseName"] == "agent-shopping-list", "Unexpected Helm release/chart")
    values = helm["values"]
    require(values["image"]["repository"] == IMAGE, "Unexpected image repository")
    require(values["hostname"] == "shopping.vvivid.dev", "Wrong environment hostname")
    recorded_provenance = values["provenance"]
    if recorded_provenance == {"sourceSha": "UNCONFIGURED"}:
        require(helm["version"] == "UNCONFIGURED" and values["image"]["digest"] == "UNCONFIGURED",
                "Inconsistent unconfigured release")
    else:
        previous = Release.parse(
            recorded_provenance["sourceRef"], recorded_provenance["sourceSha"],
            recorded_provenance["runId"], recorded_provenance["runAttempt"],
        )
        require(recorded_provenance == previous.provenance(
                    recorded_provenance["imageDigest"], recorded_provenance["chartDigest"]),
                "Inconsistent existing provenance")
        require(recorded_provenance["imageDigest"] == values["image"]["digest"] and
                recorded_provenance["chartVersion"] == helm["version"],
                "Inconsistent existing release")
        if recorded_provenance == provenance:
            return False
        require(not (previous.ref == release.ref and previous.sha == release.sha and
                     previous.run_id == release.run_id and previous.attempt == release.attempt),
                "Release digest/provenance substitution")
        require(release.order > previous.order, "Production version reuse or downgrade")
    helm["version"] = release.chart_version
    values["image"]["digest"] = image_digest
    values["provenance"] = provenance
    return True


def read_document(path):
    require(not path.is_symlink(), "Desired-state file must not be a symlink")
    return json.loads(path.read_text())
