#!/usr/bin/env python3
"""Check the actual Linux amd64 executable's protected-file and SQLite CLI paths.

No test provider or fake login is added to the image. HTTP/browser acceptance is
covered separately by the production-export tests through the real Go handler.
"""
import argparse
import base64
import io
import json
import os
from pathlib import Path
import sqlite3
import subprocess
import tarfile
import tempfile


def docker(*arguments, input=None):
    command = ["docker"]
    if os.environ.get("DOCKER_CONTEXT"):
        command += ["--context", os.environ["DOCKER_CONTEXT"]]
    return subprocess.run(command + list(arguments), input=input, stdout=subprocess.PIPE,
                          stderr=subprocess.PIPE, check=True).stdout


def archive(files):
    output = io.BytesIO()
    with tarfile.open(fileobj=output, mode="w") as tar:
        for name, content, mode in files:
            member = tarfile.TarInfo(name)
            member.size, member.mode, member.uid, member.gid = len(content), mode, 65532, 65532
            tar.addfile(member, io.BytesIO(content))
    return output.getvalue()


def start(container, *, succeeds=True):
    # `docker start --attach` is not itself evidence of the executable exit code.
    docker("start", container)
    docker("wait", container)
    state = json.loads(docker("inspect", container))[0]["State"]
    if state["Running"] or (state["ExitCode"] == 0) != succeeds:
        raise ValueError("Unexpected candidate executable exit state")


def run(image):
    metadata = json.loads(docker("image", "inspect", image))[0]
    if (metadata["Os"], metadata["Architecture"], metadata["Config"]["User"]) != (
            "linux", "amd64", "65532:65532"):
        raise ValueError("Candidate must be Linux amd64 with nonroot app UID/GID")
    containers = []

    def create(*arguments):
        container = docker("create", image, *arguments).decode().strip()
        containers.append(container)
        return container

    try:
        keygen = create("-keygen-file=/tmp/session-keys.json")
        start(keygen)
        original_keys = docker("cp", keygen + ":/tmp/session-keys.json", "-")
        with tarfile.open(fileobj=io.BytesIO(original_keys)) as tar:
            member, = tar.getmembers()
            if (member.mode, member.uid, member.gid) != (0o600, 65532, 65532):
                raise ValueError("Generated keyring must be exactly 0600 owned by the app")
            keyring = json.load(tar.extractfile(member))
            if len(base64.b64decode(keyring["keys"][keyring["active"]], validate=True)) != 32:
                raise ValueError("Generated AES key is invalid")
        start(keygen, succeeds=False)
        if docker("cp", keygen + ":/tmp/session-keys.json", "-") != original_keys:
            raise ValueError("Exclusive key generation replaced an existing keyring")
        enrollment = json.dumps({"issuer": "https://issuer.example.test", "subject":
                                 "synthetic-container-owner", "label": "Synthetic owner"}).encode()
        data = create("-db=/tmp/vocabbank/sqlite/app.sqlite", "-enroll-file=/tmp/enrollment.json")
        docker("cp", "-a", "-", data + ":/tmp", input=archive([("enrollment.json", enrollment, 0o600)]))
        start(data)
        start(data)
        exported = docker("cp", data + ":/tmp/vocabbank/sqlite", "-")
        with tarfile.open(fileobj=io.BytesIO(exported)) as tar:
            members = tar.getmembers()
            directory = next(member for member in members if member.isdir())
            database = next(member for member in members if member.name.endswith("app.sqlite"))
            if (directory.mode, directory.uid, database.mode, database.uid) != (0o700, 65532, 0o600, 65532):
                raise ValueError("SQLite directory/file must be private and app-owned")
            with tempfile.TemporaryDirectory() as temporary:
                path = Path(temporary) / "app.sqlite"
                path.write_bytes(tar.extractfile(database).read())
                with sqlite3.connect(path) as connection:
                    if (connection.execute("PRAGMA integrity_check").fetchone()[0] != "ok"
                            or connection.execute("PRAGMA foreign_key_check").fetchall()
                            or connection.execute("SELECT count(*) FROM owners").fetchone()[0] != 1
                            or connection.execute("SELECT count(*) FROM words").fetchone()[0] != 0):
                        raise ValueError("Enrollment/repeat start must retain one owner and no words")
        unsafe = create("-db=/tmp/unsafe.sqlite", "-enroll-file=/tmp/enrollment.json")
        docker("cp", "-a", "-", unsafe + ":/tmp", input=archive([("enrollment.json", enrollment, 0o644)]))
        start(unsafe, succeeds=False)
        no_identity = create("-db=/tmp/unconfigured.sqlite", "-static=/static")
        start(no_identity, succeeds=False)
        assets = docker("cp", no_identity + ":/static/index.html", "-")
        with tarfile.open(fileobj=io.BytesIO(assets)) as tar:
            member, = tar.getmembers()
            if not member.isfile() or member.size < 100:
                raise ValueError("Candidate must contain the real static export")
        print("Candidate CLI checks passed: Linux amd64/nonroot, exclusive 0600 key generation, "
              "private SQLite enrollment/repeat start/integrity, unsafe-file rejection and exported assets. "
              "This does not establish container HTTP or real-provider acceptance.")
    finally:
        for container in containers:
            docker("rm", "--force", container)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", default="vocabbank:local")
    run(parser.parse_args().image)
