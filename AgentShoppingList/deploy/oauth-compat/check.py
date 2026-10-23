#!/usr/bin/env python3
"""Verify exported source, patch a scratch copy, test/build, then prove reversal.

This tool never applies a patch to its source argument and never runs a gateway.
Go dependencies must already be cached; network module fetching is disabled.
"""

import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile


BUNDLE = Path(__file__).resolve().parent
PATCH_FILES = {
    "internal/gateway/authproxy.go",
    "internal/gateway/authproxy_test.go",
    "internal/gateway/config.go",
}
TEST_FILE = "internal/gateway/oauth_compat_test.go"
# Keep gateway tests out of the shopping module until they are staged in scratch.
TEST_ARTIFACT = "oauth_compat_test.go.txt"


def manifest(name):
    entries = {}
    for line in (BUNDLE / name).read_text().splitlines():
        digest, relative = line.split("  ", 1)
        path = Path(relative)
        allowed = relative in {"go.mod", "go.sum", "cmd/sshmcp-gateway/main.go"} or (
            path.parent.as_posix() in {"internal/gateway", "internal/runner"}
            and path.suffix == ".go"
            and not path.name.startswith(".")
        )
        if not allowed or relative in entries or len(digest) != 64 or any(c not in "0123456789abcdef" for c in digest):
            raise ValueError(f"unsafe or duplicate manifest entry: {relative}")
        entries[relative] = digest
    if not entries:
        raise ValueError(f"empty manifest: {name}")
    return entries


def verify(root, entries):
    for relative, expected in entries.items():
        path = root / relative
        for component in [path, *path.parents]:
            if component == root:
                break
            if component.is_symlink():
                raise ValueError(f"source symlink refused: {relative}")
        if not path.is_file() or hashlib.sha256(path.read_bytes()).hexdigest() != expected:
            raise ValueError(f"source hash mismatch: {relative}")


def run(command, cwd, env, capture=False):
    print("Checking:", " ".join(str(part) for part in command), flush=True)
    return subprocess.run(command, cwd=cwd, env=env, check=True, text=True,
                          stdout=subprocess.PIPE if capture else None).stdout


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("source", type=Path, help="read-only exact deployed-source export")
    parser.add_argument("--output", required=True, type=Path, help="new local directory for checked binary/report")
    args = parser.parse_args()
    source = args.source.resolve(strict=True)
    output = args.output.resolve()
    if output.exists():
        raise ValueError("output directory must not already exist")
    if source == output or source in output.parents:
        raise ValueError("output must be outside the source export")

    baseline = manifest("baseline.sha256")
    patched = manifest("patched.sha256")
    if set(patched) != PATCH_FILES | {TEST_FILE}:
        raise ValueError("unexpected patched file inventory")
    verify(source, baseline)
    headers = {line[4:] for line in (BUNDLE / "compatibility.patch").read_text().splitlines()
               if line.startswith(("--- ", "+++ "))}
    if headers != {prefix + name for name in PATCH_FILES for prefix in ("a/", "b/")}:
        raise ValueError("patch paths do not match the reviewed three-file scope")

    go = shutil.which(os.environ.get("GO", "go"))
    if not go:
        raise ValueError("Go 1.26.9 is required; set GO to its executable")
    cache = BUNDLE.parents[1] / ".cache"
    cache.mkdir(exist_ok=True)
    env = os.environ.copy()
    env.update({"GOTOOLCHAIN": "local", "GOWORK": "off", "GOENV": "off", "GOFLAGS": "",
                "GOPROXY": "off", "GOSUMDB": "off", "GOPRIVATE": "", "GONOPROXY": "", "GONOSUMDB": "",
                "GOCACHE": env.get("GOCACHE", str(cache / "go-build")),
                "GOMODCACHE": env.get("GOMODCACHE", str(cache / "go-mod"))})
    for key in ("GOCACHE", "GOMODCACHE"):
        location = Path(env[key]).resolve()
        if location == source or source in location.parents:
            raise ValueError(f"{key} must be outside the source export")
    for key in ("GOOS", "GOARCH", "CGO_ENABLED"):
        env.pop(key, None)
    version = run([go, "version"], source, env, capture=True).strip()
    if not version.startswith("go version go1.26.9 "):
        raise ValueError(f"expected Go 1.26.9, got {version}")

    with tempfile.TemporaryDirectory(prefix="oauth-compat-", dir=cache) as temporary:
        work = Path(temporary)
        (work / ".tmp").mkdir()
        env["GOTMPDIR"] = str(work / ".tmp")
        for relative in baseline:
            target = work / relative
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(source / relative, target)
        verify(work, baseline)
        patch = ["patch", "--batch", "--forward", "--fuzz=0", "-p1", "-i", str(BUNDLE / "compatibility.patch")]
        run([*patch, "--dry-run"], work, env)
        run(patch, work, env)
        shutil.copyfile(BUNDLE / TEST_ARTIFACT, work / TEST_FILE)
        verify(work, patched)

        gofmt = str(Path(go).with_name("gofmt"))
        unformatted = run([gofmt, "-l", *sorted(patched)], work, env, capture=True)
        if unformatted.strip():
            raise ValueError(f"unformatted Go files: {unformatted.strip()}")
        run([go, "vet", "./..."], work, env)
        run([go, "test", "-count=1", "./..."], work, env)
        run([go, "test", "-race", "-count=1", "./internal/gateway", "./internal/runner"], work, env)
        binary = work / "sshmcp-gateway-linux-amd64"
        build_env = env | {"GOOS": "linux", "GOARCH": "amd64", "CGO_ENABLED": "0"}
        run([go, "build", "-trimpath", "-buildvcs=false", "-o", str(binary), "./cmd/sshmcp-gateway"], work, build_env)
        build_info = run([go, "version", "-m", str(binary)], work, env, capture=True)
        binary_digest = hashlib.sha256(binary.read_bytes()).hexdigest()

        # Prove the standard patch reverses exactly in scratch, not in source.
        reverse = ["patch", "--batch", "--fuzz=0", "-R", "-p1", "-i", str(BUNDLE / "compatibility.patch")]
        run([*reverse, "--dry-run"], work, env)
        run(reverse, work, env)
        (work / TEST_FILE).unlink()
        verify(work, baseline)
        verify(source, baseline)
        output.mkdir(parents=True, exist_ok=False)
        shutil.copyfile(binary, output / binary.name)
        (output / binary.name).chmod(0o755)
        (output / "build-info.txt").write_text(build_info)
        report = {"go": version, "target": "linux/amd64", "cgo_enabled": False,
                  "binary_sha256": binary_digest,
                  "patch_sha256": hashlib.sha256((BUNDLE / "compatibility.patch").read_bytes()).hexdigest(),
                  "baseline_files": len(baseline), "source_unchanged": True,
                  "checks": ["hashes", "patch dry-run/apply", "gofmt", "vet", "all tests", "gateway/runner race", "gateway build", "reverse exact hashes"],
                  "live_changes": False, "binary_executed": False}
        (output / "report.json").write_text(json.dumps(report, indent=2) + "\n")
        print(json.dumps(report, indent=2), flush=True)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, subprocess.CalledProcessError) as error:
        raise SystemExit(str(error)) from error
