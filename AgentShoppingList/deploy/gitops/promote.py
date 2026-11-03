#!/usr/bin/env python3
"""Publish a fixed shopping environment change to the private GitOps main branch.

The CLI has no remote/path override. Git credentials are read through askpass,
never embedded in URLs, arguments or repository config. Tests use local remotes
through the lower-level transaction function.
"""

import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile

from release import (GITOPS_REPOSITORY, SOURCE_REPOSITORY, Release, ReleaseError,
                     read_document, require, update_desired_state)


def git(directory, *arguments, environment=None, check=True):
    result = subprocess.run(["git", "-c", "credential.helper=", *arguments], cwd=directory,
                            env=environment, text=True, capture_output=True)
    if check and result.returncode:
        # Git stderr can contain remote-controlled content. Keep credential-bearing
        # execution diagnostics out of Actions logs.
        raise ReleaseError("Git operation failed: " + arguments[0])
    return result


def verify_source(release, environment):
    result = git(None, "ls-remote", SOURCE_REPOSITORY, release.ref, release.ref + "^{}",
                 environment=environment)
    refs = {}
    for line in result.stdout.splitlines():
        object_sha, ref_name = line.split()
        refs[ref_name] = object_sha
    # Annotated tags report a tag object and a peeled commit; compare the commit.
    current = refs.get(release.ref + "^{}", refs.get(release.ref))
    require(current == release.sha, "Source ref moved or was deleted; refusing stale publication")


def promote_checkout(directory, release, image_digest, chart_digest, verify,
                     environment=None, before_push=lambda _attempt: None):
    """Retry against fresh main after a normal non-fast-forward rejection.

    Rebuild the one-file commit each time, preserving all unrelated remote work.
    Never force-push or rebase remote history. Revalidate source/order each retry.
    """
    directory = Path(directory)
    for attempt in range(4):
        git(directory, "fetch", "--quiet", "origin", "refs/heads/main", environment=environment)
        git(directory, "checkout", "--quiet", "--detach", "FETCH_HEAD", environment=environment)
        desired_state_path = directory / release.path
        require(desired_state_path.resolve().is_relative_to(directory.resolve()), "Desired-state path escaped checkout")
        document = read_document(desired_state_path)
        verify()
        if not update_desired_state(document, release, image_digest, chart_digest):
            return "already recorded"
        desired_state_path.write_text(json.dumps(document, indent=2) + "\n")
        git(directory, "add", "--", str(release.path), environment=environment)
        staged_paths = git(directory, "diff", "--cached", "--name-only", environment=environment).stdout.splitlines()
        require(staged_paths == [release.path.as_posix()], "Unexpected GitOps change set")
        git(directory, "-c", "user.name=shopping-release[bot]", "-c",
            "user.email=shopping-release[bot]@users.noreply.github.com", "-c", "commit.gpgsign=false",
            "commit", "--quiet", "-m", f"Deploy AgentShoppingList {release.chart_version}",
            environment=environment)
        before_push(attempt)
        verify()
        result = git(directory, "push", "--porcelain", "origin", "HEAD:refs/heads/main",
                     environment=environment, check=False)
        if result.returncode == 0:
            return "recorded"
        # Only a competing main update is retryable. Authentication/protection/
        # transport failures stop instead of being silently treated as races.
        if "[rejected]" not in result.stdout or not any(
                reason in result.stdout for reason in ("fetch first", "non-fast-forward")):
            raise ReleaseError("GitOps push failed (authentication, protection or transport)")
    raise ReleaseError("GitOps main kept changing; retry the release after inspecting desired state")


def authenticated_environment(directory, token):
    askpass = Path(directory) / "askpass"
    askpass.write_text('#!/bin/sh\ncase "$1" in\n*Username*) printf "%s\\n" x-access-token ;;\n'
                       '*Password*) printf "%s\\n" "$GIT_AUTH_TOKEN" ;;\n*) exit 1 ;;\nesac\n')
    askpass.chmod(0o700)
    environment = dict(os.environ, GIT_ASKPASS=str(askpass), GIT_AUTH_TOKEN=token,
                       GIT_TERMINAL_PROMPT="0")
    # Avoid inherited tracing that could log HTTP authorization headers.
    for name in list(environment):
        if name.startswith("GIT_TRACE") or name == "GIT_CURL_VERBOSE":
            environment.pop(name)
    return environment


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--ref", required=True)
    parser.add_argument("--sha", required=True)
    parser.add_argument("--run-id", required=True)
    parser.add_argument("--attempt", required=True)
    parser.add_argument("--image-digest", required=True)
    parser.add_argument("--chart-digest", required=True)
    arguments = parser.parse_args()
    release = Release.parse(arguments.ref, arguments.sha, arguments.run_id, arguments.attempt)
    # Validate artifact inputs before any Git operation.
    release.provenance(arguments.image_digest, arguments.chart_digest)
    require(os.environ.get("GITOPS_TOKEN"), "A scoped GitHub App installation token is required")
    require(os.environ.get("SOURCE_TOKEN"), "A source read token is required")
    with tempfile.TemporaryDirectory(prefix="shopping-gitops-") as directory:
        root = Path(directory)
        source_auth = root / "source-auth"
        source_auth.mkdir(mode=0o700)
        target_auth = root / "target-auth"
        target_auth.mkdir(mode=0o700)
        source_environment = authenticated_environment(source_auth, os.environ["SOURCE_TOKEN"])
        target_environment = authenticated_environment(target_auth, os.environ["GITOPS_TOKEN"])
        verify = lambda: verify_source(release, source_environment)
        verify()
        checkout = root / "repo"
        git(None, "clone", "--quiet", "--no-checkout", "--single-branch", "--branch", "main",
            GITOPS_REPOSITORY, str(checkout), environment=target_environment)
        result = promote_checkout(checkout, release, arguments.image_digest,
                                  arguments.chart_digest, verify, target_environment)
        print(f"AgentShoppingList {release.channel}: {result}")


if __name__ == "__main__":
    try:
        main()
    except (ReleaseError, KeyError, ValueError) as error:
        raise SystemExit(str(error)) from None
