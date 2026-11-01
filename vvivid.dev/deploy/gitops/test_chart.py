#!/usr/bin/env python3
"""Lint/render both website environments with explicit, nondeployable fixtures."""

import json
import os
from pathlib import Path
import subprocess
import tempfile


def main():
    chart = Path(__file__).resolve().parents[2] / "chart"
    helm = os.environ.get("HELM", "helm")
    with tempfile.TemporaryDirectory(prefix="website-chart-check-") as directory:
        values_path = Path(directory) / "fixture.json"
        for channel, hostname in (("staging", "preview.vvivid.dev"), ("production", "vvivid.dev")):
            values = {"hostname": hostname, "image": {"digest": "sha256:" + "a" * 64},
                      "provenance": {"sourceSha": "b" * 40}}
            values_path.write_text(json.dumps(values))
            subprocess.run([helm, "lint", "--strict", str(chart), "-f", str(values_path)], check=True)
            for exposure in ([], ["--set", "tls.productionEnabled=true", "--set", "ingress.enabled=true"]):
                subprocess.run([helm, "template", "vvivid-website", str(chart), "--namespace", "website-" + channel,
                                "-f", str(values_path), *exposure], check=True, stdout=subprocess.DEVNULL)
            invalid = subprocess.run([helm, "template", "vvivid-website", str(chart), "-f", str(values_path),
                                      "--set", "ingress.enabled=true"], capture_output=True)
            if invalid.returncode == 0:
                raise SystemExit("Public ingress accepted without the production-certificate gate")
        unconfigured = subprocess.run([helm, "template", "vvivid-website", str(chart)], capture_output=True)
        if unconfigured.returncode == 0:
            raise SystemExit("Unconfigured chart unexpectedly rendered")
    print("Both chart environments passed lint/render and activation-gate checks (fixture digests only).")


if __name__ == "__main__":
    main()
