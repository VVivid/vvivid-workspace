#!/usr/bin/python3
"""Forced SSH command: no shell, forwarding, or caller-selected root command."""

import os
import re
import sys


def main():
    command = os.environ.get("SSH_ORIGINAL_COMMAND", "")
    match = re.fullmatch(r"deploy (sha256:[0-9a-f]{64})", command)
    if match is None:
        print("Only 'deploy sha256:<64 lowercase hex digits>' is accepted", file=sys.stderr)
        return 1
    os.execv("/usr/bin/sudo", ["sudo", "-n", "/usr/local/sbin/vvivid-live-deploy", match[1]])


if __name__ == "__main__":
    sys.exit(main())
