# X-Trace stale cat example

A small prototype inspired by [the X-Trace paper](fonseca.pdf): follow one image request across independently instrumented components and identify the replica that served an old image.

```text
browser → Nginx (cache disabled) → Traefik (round robin) → Go replica A or B
              │                         │                      │
              └────────────── POST /reports ────────────────────┘
                                      ↓
                               Go collector → SQLite
```

There are exactly five containers. The Nginx container uses **OpenResty**, a Nginx distribution with Lua support. Traefik uses the actual local Go middleware plugin in `traefik-plugin/`; its built-in load balancer chooses between two equal backend servers. Configuration comes from a file, with no Docker socket.

## Run

Install Docker with Compose, then run from this directory:

```sh
docker compose up --build
```

Open **http://localhost:8080** and click **Request a cat** several times. With both replicas available and sequential requests, Traefik distributes requests between A and B; the initial replica is not significant.

- **A** seeds its in-process image cache once at startup with that startup's current UTC timestamp printed inside the SVG.
- **B** seeds the same cat drawing once at startup with a timestamp 24 hours old.

Both serve their cached image unchanged on every request and report `cache_hit=true`. Nginx proxy caching and browser caching are disabled. This is a controlled version contrast: A is the current startup version and B is one day behind. Neither image refreshes while its process runs; restarting a replica seeds its cache again. `IMAGE_AGE` is `0s` for A and `24h` for B.

Each response exposes `X-XTrace-TaskID` and `X-Worktree-URL`. The page shows both, including an **Open worktree** link. Reports arrive asynchronously, so refresh the worktree if it initially appears incomplete. You can also inspect response headers directly:

```sh
curl -sS -D - http://localhost:8080/cat.svg -o /tmp/cat.svg
```

The collector is at **http://localhost:8081/worktree?taskID=YOUR_TASK_ID**. Both published ports bind to loopback.

## What a worktree means

Nginx starts a fresh `TaskID` and root `OpID` for each `/cat.svg` request. Traefik keeps the task, makes a new operation, and sets `ParentID` to Nginx's operation. The selected replica creates another operation with Traefik as its parent. Both child edges are `NEXT`.

The headers `X-XTrace-TaskID`, `X-XTrace-OpID`, `X-XTrace-ParentID`, and `X-XTrace-EdgeType` carry context. Each component separately posts its own report, including its local timestamp and details. The replica records its name, cache hit, and the timestamp baked into the actual image.

The collector reconstructs edges using IDs, independent of clock skew or report arrival order. It shows missing parent placeholders and marks observations incomplete unless it sees the expected Nginx → Traefik → one replica chain. A missing report may be delayed, dropped, or reflect a request that never reached that hop. It does not by itself prove which component failed.

Go reporting uses one worker, a queue of 128 reports, and a 500 ms HTTP timeout. Nginx uses asynchronous Lua timers, bounded to 128 pending and 16 running, with 300 ms socket operation timeouts. Reporting failures are logged; there are no retries. Full queues, outages, or process shutdown can lose reports without delaying image delivery.

SQLite stores accepted reports in the Compose named volume `reports`. Identical operation duplicates are accepted; conflicting duplicates are rejected. `docker compose down` stops the example and retains that volume. `docker compose down -v` also deletes its stored traces. Replica caches and pending report queues live only in memory.

## Read and check

Start with `compose.yaml` and `nginx/trace.lua`, then `traefik-plugin/plugin.go`, `replica.go`, and `collector.go` / `worktree.go`. `traefik-plugin/report.go` contains the shared wire format and bounded reporter; the plugin module uses Go 1.20 syntax for Traefik's interpreter. The Go application targets Go 1.26 and uses `go-sqlite3`, so local builds need a C compiler; the Docker build includes one.

```sh
go test ./...
go -C traefik-plugin test ./...
go build -o /tmp/xtrace-demo .
docker compose config
```

Tests cover persisted reports, invalid input, duplicate conflicts, task isolation, out-of-order / skewed-clock reconstruction, missing parents, cycles, escaped HTML, fresh / stale image bytes, causal propagation, and bounded asynchronous reporting. Tests use an in-memory HTTP transport and do not start the proxies.

During development, Go tests (including the race detector) and builds, YAML parsing / static checks, Lua syntax, and browser JavaScript syntax were checked. A temporary check using Yaegi v0.16.1 (the version in Traefik 3.6.2) executed the plugin, advanced its causal headers, and delivered its independent report through an in-memory HTTP transport. Docker and a usable container runtime were unavailable, and sandbox restrictions prevented listening on TCP ports, so the complete Compose stack was **not run**. A native Traefik attempt parsed its static configuration but stopped at the restricted listener; this does not validate the complete proxy path.

Configuration references: [Traefik local plugins](https://doc.traefik.io/traefik/reference/install-configuration/experimental/plugins/), [Traefik load balancing](https://doc.traefik.io/traefik/reference/routing-configuration/http/load-balancing/service/), and [OpenResty Lua timers](https://github.com/openresty/lua-nginx-module#ngxtimerat).
