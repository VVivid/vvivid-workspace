# Private hosting and recovery

The [image](../Dockerfile), [Helm chart](../deploy/helm/artificial-memory/Chart.yaml),
and [illustrative values](../deploy/examples/values.yaml) prepare one private
SQLite writer. Local synthetic checks cover these artifacts. No image publication,
cluster installation, gateway configuration, provider login, or real enrollment
has been performed by preparing them.

## Local validation and image

Use Go 1.26.9, Make, Docker with BuildKit, Helm 3, and Python 3 with the exact
packages in [requirements.txt](../deploy/requirements.txt). Run from this project:

```sh
make check test race build integration demo chart-test
make container-build
make container-test
```

`container-build` uses only this directory as its build context. Both base images
are pinned by digest. Only the production MCP/admin executable enters the final
distroless image; examples, tests, Git metadata, fixtures, and session CA mounts
are excluded. `TEST_IMAGE` defaults to `artificial-memory:local`. `VERSION` and
`COMMIT` label a local build; no target pushes an image or deploys it.

In a managed environment that intercepts TLS, pass the public combined CA bundle
as a temporary BuildKit secret, preserving normal proxy build arguments:

```sh
make container-build BUILD_CA_FILE=/etc/ssl/certs/ca-certificates.crt
```

The bundle must include the session's proxy CA and ordinary public roots. Keep TLS
verification enabled. If the Docker configuration directory is read-only, select
a writable `BUILDX_CONFIG` directory for builder state; preserve `DOCKER_CONFIG`
and its registry authentication. The CA is mounted only for module download and
does not enter any image layer.

The image defaults to UID/GID 65532, `MEMORY_LISTEN=:8080`, and
`MEMORY_DATABASE=/data/sqlite/memory.db`. Run with a read-only root filesystem,
all capabilities dropped, no privilege escalation, and an exclusively owned
persistent `/data` mount writable by UID 65532. `/data/sqlite` must be private
0700 and database files 0600 or stricter. Only `/data` needs to be writable.
SQLite explicitly uses `temp_store=MEMORY` for temporary sorts; `TMPDIR` and
`SQLITE_TMPDIR` point to the same private directory for any temporary files.
Size memory limits for the bounded concurrent query workload and backup operation.

`artificial-memory healthcheck live|ready` contacts only loopback, using the port
from `MEMORY_LISTEN`, with a two-second timeout, no proxy, redirect, bearer token,
JWKS loading, or database opening. IPv6 listeners probe `::1`; other listeners
probe `127.0.0.1`. Listen on all interfaces or the appropriate loopback address
for these probes. Ready delegates storage checking to the running HTTP service.
SIGTERM drains HTTP work for up to ten seconds before closing SQLite; allow at
least fifteen seconds before forceful termination.

## Helm and existing infrastructure

Copy and replace every illustrative value with operator-verified inputs. Empty
defaults deliberately fail validation. Supply the published image repository and
digest, existing namespace, storage class, node selector, canonical backend
origin, original issuer, existing gateway issuer/public JWKS ConfigMap, and exact
client allowlist. Read [AUTH.md](AUTH.md) before configuring trust or enrollment.
The ConfigMap's selected key contains public RSA JWKS only. Key changes require
a rollout to reload the pinned keys.

An offline preview, using a private operator values file, is:

```sh
helm lint --strict deploy/helm/artificial-memory -f /secure/memory-values.yaml
helm template artificial-memory deploy/helm/artificial-memory \
  --namespace existing-memory-namespace -f /secure/memory-values.yaml
```

These commands do not install. The chart creates only its Deployment, ClusterIP
Service, and, unless `persistence.existingClaim` is set, retained PVC. It creates
no Namespace, issuer, credential Secret, signing key, gateway, tunnel, ingress,
load balancer, or RBAC. The gateway public ConfigMap and namespace must already
exist. Kubernetes admission and storage behavior still require environment-specific
verification before a separately authorized deployment.

`replicaCount` accepts one writer or zero for maintenance. Recreate avoids a
surge writer during upgrades. ReadWriteOnce does not exclude two writers on the
same node: never mount this claim into another service replica/release, and never
add an autoscaler. Keep the same release name or explicitly select the retained
claim when reinstalling. Helm's keep annotation preserves a chart-created PVC on
uninstall, but it is not a backup and does not change a StorageClass/PV reclaim
policy. Never delete the claim as part of routine rollout or recovery.

The volume must provide local/block-filesystem SQLite WAL locking and reliable
fsync, accessible on the operator-selected node. A storage driver must prepare
the volume root for GID 65532 and honor `fsGroupChangePolicy: OnRootMismatch` (or
provide equivalent ownership without recursively changing existing files).
The first writer creates `/data/sqlite` as UID 65532/mode 0700; on restart the
matching volume root must prevent recursive fsGroup processing from changing
the database to 0660. A driver that always grants group access to files is
incompatible: startup refuses nonprivate database files. Do not relax file
checks to accommodate it. Verify actual provisioner behavior across pod
recreation before entrusting real memories. Remote/NFS/shared-writer storage
and transparent failover are outside this proof of concept.

Service traffic and health endpoints must stay on trusted private networking.
ClusterIP alone does not restrict other cluster workloads; use the existing
cluster's network controls to allow the gateway and operator access. The
[gateway route fragment](../deploy/examples/gateway-route.json) matches the
illustrative values: exact `upstream_host` equals the backend origin's Host,
audience `artificial-memory-mcp`, required read scope, and forwarding only granted
read/write scopes. The external OAuth resource is the public gateway origin plus
`/artificial-memory/mcp`, not the backend audience. Merge a reviewed route into
the existing gateway only under separately authorized infrastructure work.

Enrollment stays a local operator command. A subject file must be a regular
0600 file owned/readable by the executing operator UID (65532 inside this image);
fsGroup-readable 0640/0660 projected files are rejected. Arrange temporary
private file ownership through the operator's existing secure provisioning
process, then invoke `admin enroll` as documented in [AUTH.md](AUTH.md). The chart
does not create or seed an account. Remove only the temporary subject file after
the operator finishes; never place it in a public JWKS ConfigMap or command line.

## Consistent backup

Choose retention, backup location, access controls, encryption, and a separate
failure domain according to the operator's needs. This project does not create
remote storage or schedules. Snapshots contain all account memories, original
identity subjects, replay data, and cursor signing settings; protect them as
private data. Keep multiple verified generations and retain the pre-cutover
volume until recovery is accepted.

Use the production binary as the journal's filesystem owner. The source must
already exist as a private regular file at current application/schema v2. The
output path must be new, inside a private 0700 directory. One existing service
writer may stay open for backup:

```sh
bin/artificial-memory admin backup \
  --database /private/sqlite/memory.db \
  --output /private/backups/memory-2026-11-07.db
```

The command uses SQLite `VACUUM INTO`, including committed WAL pages in one
consistent snapshot. It verifies application/schema/settings, SQLite integrity
and foreign keys, and shared/per-space table contracts before reporting success.
The private output includes immutable schema guards, FTS, typed links, original
account/enrollment state, complete idempotency results and the cursor key. It
fsyncs both output and parent directory. The source is opened read-only; the
command never initializes a missing source. Failed attempts remove only their
own newly reserved output file. Existing targets, symlinks, source aliases and
orphan destination sidecars are refused. Default deadline is five minutes;
`--timeout 15m` allows an operator-selected larger bound.

Allow storage/time for the output and SQLite's work. Heavy snapshot work may
affect service latency; quiesce the writer for a predictable maintenance window
when needed. A lost write acknowledgment requires retrying the same input/key
and client. Never back up only a live SQLite main file with `cp`; uncheckpointed
commits can still reside in its WAL. Copy or export only a completed snapshot,
using the operator's approved secure mechanism.

## Isolated restore and explicit cutover

Restore always creates a NEW private path and verifies the same integrity,
application and schema contracts. It never overwrites an existing file, attaches
to an active service, deletes a memory, or changes the original backup:

```sh
bin/artificial-memory admin restore \
  --backup /private/backups/memory-2026-11-07.db \
  --database /private/recovery/sqlite/memory.db
```

Open the restored journal in isolation and verify expected record IDs, source
citations, rejected reasons, FTS/tag/date retrieval, links, exact JSON values and
idempotent retry. Existing signed cursors continue to address their captured
snapshot because the account IDs and cursor key are preserved. Enrollments retain
their snapshot-time active/disabled state; apply any later revocations before
allowing gateway traffic. A backup captures no later source writes. A restore
for the current binary rejects unsupported schema versions instead of migrating
them silently; use a separately reviewed migration procedure when needed.

For k3s, provision a fresh compatible PVC, restore into its new
`/data/sqlite/memory.db` as UID 65532, and verify it with an isolated operator
instance using the same pinned trust. At final cutover, stop/quiesce the original
writer (`replicaCount: 0`), capture any needed final snapshot, switch
`persistence.existingClaim` to the new verified PVC, and start one writer. Retain
the original claim untouched. Confirm readiness and authorized retrieval before
reopening gateway traffic. A rollback requires quiescing the replacement writer
and explicitly selecting the retained original claim; decide how any post-cutover
writes are preserved before switching. These are operator instructions, not
actions performed by the build or test targets.

## Synthetic proof boundaries

`make demo` creates a temporary private database, loopback HTTP listener and
ephemeral RSA signing key, enrolls an invented identity, and uses the official
SDK against the real service/authentication boundary. It prints original Go and
SQLite reasons, rejected PostgreSQL alternatives, PAYM-1234's cross-space reuse,
and a later superseding PostgreSQL decision with synthetic source citations.
Topic/tag/keyword/date filters preserve older ancestors and later replacement
references. All data and listeners are owned by the example and cleaned up on
exit. There are no real credentials or real user memories.

`make container-test` requires the locally built image and the pinned Go helper
base already available to Docker. It runs app/admin containers with the image's
default nonroot UID, read-only root, dropped capabilities, no privilege escalation,
and an owned named volume. A root helper only prepares that temporary volume and
copies a synthetic subject file. Actual signed SDK calls, anonymous/write-scope
rejection, SIGTERM, a new container on the same volume, preserved receipts/history/
cursors/enrollment and 0700/0600 storage permissions are checked. The exported
runtime filesystem is checked for the production binary and absence of example
signers, fixtures, build source and session CA. Only test-owned containers and
volumes are removed. These checks do not establish real provider login, k3s
volume-driver compatibility, publication, or live deployment.
