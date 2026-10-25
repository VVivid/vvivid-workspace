#!/usr/bin/python3
"""Deploy one immutable image, scoped by the root-owned installed executable."""

import fcntl
import json
import os
from pathlib import Path
import re
import signal
import stat
import subprocess
import sys
import tempfile
import time
from urllib.parse import urlsplit


IMAGE_REPOSITORY = "ghcr.io/vvivid/vvivid-website"
COMPOSE_FILE = Path("/home/admin/infrastructure/compose.yml")
ENV_FILE = COMPOSE_FILE.with_name(".env")
COMPOSE_PROJECT = "private-edge"
# Install this same source under each fixed name. The SSH caller cannot select
# a scope: each account's sudo rule allows only its root-owned executable.
SCOPE = "preview" if Path(sys.argv[0]).name == "vvivid-preview-deploy" else "live"
SERVICE = f"vvivid-{SCOPE}"
IMAGE_KEY = f"VVIVID_{SCOPE.upper()}_IMAGE"
HOSTNAME = "preview.vvivid.dev" if SCOPE == "preview" else "vvivid.dev"
HEALTH_URL_FILE = Path(f"/etc/vvivid-{SCOPE}/health-url")
# Both services atomically replace the same .env; separate locks lose updates.
LOCK_FILE = Path("/run/lock/vvivid-deployment.lock")
IMAGE_SETTING = re.compile(r"^\s*(?:export\s+)?" + IMAGE_KEY + r"\s*=")
DIGEST = re.compile(r"sha256:[0-9a-f]{64}")
COMMAND_ENV = {"PATH": "/usr/sbin:/usr/bin:/sbin:/bin", "DOCKER_CONFIG": "/root/.docker"}


class DeploymentError(Exception):
    pass


def run(*args, image=None, timeout=90):
    environment = dict(COMMAND_ENV)
    if image is not None:
        environment[IMAGE_KEY] = image
    try:
        result = subprocess.run(
            args, env=environment, capture_output=True, text=True, timeout=timeout
        )
    except (OSError, subprocess.TimeoutExpired) as error:
        raise DeploymentError(f"{args[0]} could not complete") from error
    if result.returncode:
        # Compose configuration and registry errors can contain credentials or hostnames.
        raise DeploymentError(f"{' '.join(args[:2])} failed (exit {result.returncode})")
    return result.stdout.strip()


def compose(*args, image=None):
    return run(
        "docker", "compose", "--project-name", COMPOSE_PROJECT,
        "--env-file", str(ENV_FILE), "--file", str(COMPOSE_FILE),
        *args, image=image,
    )


def configured_image(image=None):
    """Resolve this service's image, overriding only its dedicated setting."""
    configuration = json.loads(compose("config", "--format", "json", image=image))
    return configuration["services"][SERVICE]["image"]


def running_image():
    """Return both the configured reference and Docker's actual image ID."""
    container = compose("ps", "--quiet", "--status", "running", SERVICE)
    if not container or "\n" in container:
        raise DeploymentError(f"Expected exactly one running {SCOPE} container")
    values = run(
        "docker", "inspect", "--format", "{{.Config.Image}}\n{{.Image}}\n{{.State.Running}}",
        container,
    ).splitlines()
    if len(values) != 3 or values[2] != "true":
        raise DeploymentError(f"{SCOPE.capitalize()} container is not running")
    return values[0], values[1]


def image_setting():
    """Keep the original assignment line for rollback; None means the key was absent."""
    settings = [line for line in ENV_FILE.read_text().splitlines(keepends=True)
                if IMAGE_SETTING.match(line)]
    if len(settings) > 1:
        raise DeploymentError(f"Duplicate {IMAGE_KEY} settings; resolve before deployment")
    return settings[0] if settings else None


def write_image_setting(setting):
    """Replace only this service's key, preserving other values and file metadata."""
    metadata = ENV_FILE.stat()
    lines = [line for line in ENV_FILE.read_text().splitlines(keepends=True)
             if not IMAGE_SETTING.match(line)]
    if setting is not None:
        if lines and not lines[-1].endswith("\n"):
            lines[-1] += "\n"
        lines.append(setting if setting.endswith("\n") else setting + "\n")
    temporary = None
    try:
        with tempfile.NamedTemporaryFile(mode="w", dir=ENV_FILE.parent, delete=False) as stream:
            temporary = Path(stream.name)
            os.fchown(stream.fileno(), metadata.st_uid, metadata.st_gid)
            os.fchmod(stream.fileno(), stat.S_IMODE(metadata.st_mode))
            stream.writelines(lines)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, ENV_FILE)
    finally:
        if temporary is not None:
            temporary.unlink(missing_ok=True)


def local_health_url():
    metadata = HEALTH_URL_FILE.stat()
    if metadata.st_uid != 0 or metadata.st_mode & 0o022 or not stat.S_ISREG(metadata.st_mode):
        raise DeploymentError("Health URL must be a root-owned file without group/other writes")
    url = HEALTH_URL_FILE.read_text().strip()
    parts = urlsplit(url)
    if (parts.scheme != "http" or not parts.hostname or parts.username or parts.password
            or parts.path not in ("", "/") or parts.query or parts.fragment):
        raise DeploymentError("Health URL must be the local Traefik HTTP origin")
    return url.rstrip("/") + "/"


def check_http(url, image):
    separator = "&" if "?" in url else "?"
    status = run(
        "curl", "--silent", "--show-error", "--max-time", "5", "--noproxy", "*",
        "--header", f"Host: {HOSTNAME}", "--header", "Cache-Control: no-cache",
        "--output", "/dev/null", "--write-out", "%{http_code}",
        url + separator + "deployment=" + image.rsplit(":", 1)[-1], timeout=10,
    )
    if status != "200":
        raise DeploymentError("Website health check did not return HTTP 200")


def wait_healthy(image, image_id, local_url):
    for attempt in range(12):
        try:
            if running_image() != (image, image_id):
                raise DeploymentError(f"Running {SCOPE} image does not match the requested image")
            check_http(local_url, image)
            check_http(f"https://{HOSTNAME}/", image)
            return
        except DeploymentError:
            if attempt == 11:
                raise
            time.sleep(5)


def interrupted(signum, _frame):
    raise DeploymentError(f"Deployment interrupted by signal {signum}")


def deploy(digest):
    if not DIGEST.fullmatch(digest):
        raise DeploymentError("Expected one lowercase sha256 image digest")
    candidate = IMAGE_REPOSITORY + "@" + digest
    with LOCK_FILE.open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        local_url = local_health_url()
        previous_setting_line = image_setting()
        previous_image, previous_image_id = running_image()
        if configured_image() != previous_image:
            raise DeploymentError(f"{SCOPE.capitalize()} Compose image differs from the running image; reconcile first")
        if configured_image(candidate) != candidate:
            raise DeploymentError(f"Install the separate {SCOPE}-image Compose setting first")

        # Failed GHCR access must leave the working service and .env intact.
        run("docker", "pull", candidate, timeout=300)
        candidate_image_id = run("docker", "image", "inspect", "--format", "{{.Id}}", candidate)
        print(f"Deploying {candidate}", flush=True)
        try:
            write_image_setting(f"{IMAGE_KEY}={candidate}\n")
            compose("up", "--detach", "--no-deps", "--pull", "never", SERVICE)
            wait_healthy(candidate, candidate_image_id, local_url)
        except BaseException as failure:
            # Ignore further termination while restoring the last known working service.
            for signum in (signal.SIGTERM, signal.SIGINT, signal.SIGHUP):
                signal.signal(signum, signal.SIG_IGN)
            print(f"Deployment failed; restoring previous {SCOPE} image", file=sys.stderr, flush=True)
            try:
                write_image_setting(previous_setting_line)
                compose("up", "--detach", "--no-deps", "--pull", "never", SERVICE,
                        image=previous_image)
                wait_healthy(previous_image, previous_image_id, local_url)
            except BaseException as rollback_failure:
                raise DeploymentError("ROLLBACK FAILED; administrator intervention required") from rollback_failure
            raise DeploymentError(f"Deployment failed; previous {SCOPE} image restored") from failure
        print(f"{SCOPE.capitalize()} image healthy: {candidate}", flush=True)


def main():
    if os.geteuid() != 0 or len(sys.argv) != 2:
        raise DeploymentError("Run as root with exactly one image digest")
    for signum in (signal.SIGTERM, signal.SIGINT, signal.SIGHUP):
        signal.signal(signum, interrupted)
    deploy(sys.argv[1])


if __name__ == "__main__":
    try:
        main()
    except (DeploymentError, OSError, ValueError, KeyError) as error:
        print(f"{SCOPE.capitalize()} deployment failed: {error}", file=sys.stderr)
        sys.exit(1)
