#!/usr/bin/env python3
"""Check the exact effective gateway export in scratch; never run a gateway.

Pinned public modules must already be cached. Network module fetching is off;
Go module hashes/checksum verification remain enabled.
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
PATCH_FILES = {"internal/gateway/authproxy.go", "internal/gateway/config.go"}
TEST_FILE = "internal/gateway/newsfeed_compat_test.go"
TEST_ARTIFACT = "newsfeed_compat_test.go.txt"
EXPECTED_BASELINE_BINARY = "0c7590d6a6b52bb68a33794105233761f3868dded3d5de91e2dbedc4ce67d917"


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def manifest(name):
    entries = {}
    for line in (BUNDLE / name).read_text().splitlines():
        checksum, relative = line.split("  ", 1)
        path = Path(relative)
        allowed = relative in {"go.mod", "go.sum", "cmd/sshmcp-gateway/main.go"} or (
            path.parent.as_posix() in {"internal/gateway", "internal/runner"}
            and path.suffix == ".go" and not path.name.startswith(".")
        )
        if not allowed or path.as_posix() != relative or relative in entries or len(checksum) != 64 or any(c not in "0123456789abcdef" for c in checksum):
            raise ValueError(f"unsafe or duplicate manifest entry: {relative}")
        entries[relative] = checksum
    if not entries:
        raise ValueError(f"empty manifest: {name}")
    return entries


def verify_files(root, entries):
    for relative, checksum in entries.items():
        path = root / relative
        for component in [path, *path.parents]:
            if component == root:
                break
            if component.is_symlink():
                raise ValueError(f"source symlink refused: {relative}")
        if not path.is_file() or digest(path) != checksum:
            raise ValueError(f"source hash mismatch: {relative}")


def verify_export(root, entries):
    inventory = set()
    for directory, directories, files in os.walk(root, followlinks=False):
        for name in directories + files:
            path = Path(directory) / name
            if path.is_symlink():
                raise ValueError(f"source symlink refused: {path.relative_to(root)}")
        inventory.update((Path(directory) / name).relative_to(root).as_posix() for name in files)
    if inventory != set(entries):
        raise ValueError("source must contain exactly the effective-baseline manifest files")
    verify_files(root, entries)


def run(command, cwd, env, capture=False):
    print("Checking:", " ".join(str(part) for part in command), flush=True)
    return subprocess.run(command, cwd=cwd, env=env, check=True, text=True,
                          stdout=subprocess.PIPE if capture else None).stdout


def build(go, work, env, name):
    binary = work / name
    build_env = env | {"GOOS": "linux", "GOARCH": "amd64", "GOAMD64": "v1", "CGO_ENABLED": "0"}
    run([go, "build", "-trimpath", "-buildvcs=false", "-o", str(binary), "./cmd/sshmcp-gateway"], work, build_env)
    info = run([go, "version", "-m", str(binary)], work, env, capture=True)
    return binary, info, digest(binary)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("source", type=Path, help="exact credential-free effective gateway/runner export")
    parser.add_argument("--output", required=True, type=Path, help="new directory outside source for both binaries/report")
    args = parser.parse_args()
    if args.source.is_symlink():
        raise ValueError("source symlink refused")
    source = args.source.resolve(strict=True)
    output = args.output.resolve()
    if output.exists() or source == output or source in output.parents:
        raise ValueError("output must be new and outside the source export")
    baseline = manifest("baseline.sha256")
    patched = manifest("patched.sha256")
    if len(baseline) != 24 or "internal/gateway/oauth_compat_test.go" not in baseline or TEST_FILE in baseline:
        raise ValueError("baseline must identify the exact existing shopping-compatible export")
    if set(patched) != PATCH_FILES | {TEST_FILE}:
        raise ValueError("unexpected patched file inventory")
    verify_export(source, baseline)
    patch_file = BUNDLE / "compatibility.patch"
    headers = {line[4:] for line in patch_file.read_text().splitlines() if line.startswith(("--- ", "+++ "))}
    if headers != {prefix + name for name in PATCH_FILES for prefix in ("a/", "b/")}:
        raise ValueError("patch paths exceed the two-file newsfeed scope")
    go = shutil.which(os.environ.get("GO", "go"))
    if not go:
        raise ValueError("Go 1.26.9 is required; set GO to its executable")
    cache = BUNDLE.parents[1] / ".development" / "private-feed" / "oauth-check-cache"
    env = os.environ.copy()
    env.update({"GOTOOLCHAIN": "local", "GOWORK": "off", "GOENV": "off", "GOFLAGS": "",
                "GOEXPERIMENT": "", "GOPROXY": "off", "GOSUMDB": "sum.golang.org",
                "GOPRIVATE": "", "GONOPROXY": "", "GONOSUMDB": "",
                "GOCACHE": env.get("GOCACHE", str(cache / "go-build")),
                "GOMODCACHE": env.get("GOMODCACHE", str(cache / "go-mod"))})
    for location in [cache, Path(env["GOCACHE"]), Path(env["GOMODCACHE"])]:
        resolved = location.resolve()
        if resolved == source or source in resolved.parents:
            raise ValueError("scratch and caches must be outside the source export")
    for name in ("GOOS", "GOARCH", "GOAMD64", "CGO_ENABLED"):
        env.pop(name, None)
    version = run([go, "version"], source, env, capture=True).strip()
    if not version.startswith("go version go1.26.9 "):
        raise ValueError(f"expected Go 1.26.9, got {version}")
    cache.mkdir(parents=True, exist_ok=True)
    try:
        with tempfile.TemporaryDirectory(prefix="checked-", dir=cache) as temporary:
            work = Path(temporary)
            for relative in baseline:
                target = work / relative
                target.parent.mkdir(parents=True, exist_ok=True)
                shutil.copyfile(source / relative, target)
            verify_files(work, baseline)
            (work / ".tmp").mkdir()
            env["GOTMPDIR"] = str(work / ".tmp")
            run([go, "mod", "verify"], work, env)
            rollback, rollback_info, rollback_hash = build(go, work, env, "sshmcp-gateway-baseline-linux-amd64")
            if rollback_hash != EXPECTED_BASELINE_BINARY:
                raise ValueError(f"rebuilt current baseline does not match the verified installed binary: {rollback_hash}")
            apply = ["patch", "--batch", "--forward", "--fuzz=0", "-p1", "-i", str(patch_file)]
            run([*apply, "--dry-run"], work, env)
            run(apply, work, env)
            shutil.copyfile(BUNDLE / TEST_ARTIFACT, work / TEST_FILE)
            verify_files(work, baseline | patched)
            gofmt = str(Path(go).with_name("gofmt"))
            unformatted = run([gofmt, "-l", *sorted(name for name in baseline | patched if name.endswith(".go"))], work, env, capture=True)
            if unformatted.strip():
                raise ValueError(f"unformatted Go files: {unformatted.strip()}")
            run([go, "vet", "./..."], work, env)
            run([go, "test", "-count=1", "./..."], work, env)
            run([go, "test", "-race", "-count=1", "./internal/gateway", "./internal/runner"], work, env)
            candidate, candidate_info, candidate_hash = build(go, work, env, "sshmcp-gateway-newsfeed-linux-amd64")
            reverse = ["patch", "--batch", "--fuzz=0", "-R", "-p1", "-i", str(patch_file)]
            run([*reverse, "--dry-run"], work, env)
            run(reverse, work, env)
            (work / TEST_FILE).unlink()
            verify_files(work, baseline)
            verify_export(source, baseline)
            output.mkdir(parents=True, exist_ok=False)
            for binary, info in [(candidate, candidate_info), (rollback, rollback_info)]:
                target = output / binary.name
                shutil.copyfile(binary, target)
                target.chmod(0o755)
                (output / (binary.name + ".build-info.txt")).write_text(info)
            report = {"go": version, "target": "linux/amd64", "cgo_enabled": False,
                      "candidate_sha256": candidate_hash, "baseline_binary_sha256": rollback_hash,
                      "baseline_matches_verified_installed_binary": True,
                      "patch_sha256": digest(patch_file), "test_artifact_sha256": digest(BUNDLE / TEST_ARTIFACT),
                      "baseline_manifest_sha256": digest(BUNDLE / "baseline.sha256"),
                      "patched_manifest_sha256": digest(BUNDLE / "patched.sha256"),
                      "baseline_files": len(baseline), "source_unchanged": True,
                      "checks": ["exact source inventory/hashes", "module verify", "exact rollback rebuild", "zero-fuzz patch apply", "gofmt", "vet", "all gateway/runner tests", "gateway/runner races", "candidate build", "reverse exact hashes"],
                      "live_changes": False, "binaries_executed": False}
            (output / "report.json").write_text(json.dumps(report, indent=2) + "\n")
            print(json.dumps(report, indent=2), flush=True)
    finally:
        verify_export(source, baseline)


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, subprocess.CalledProcessError) as error:
        raise SystemExit(str(error)) from error
