# AgentShoppingList

V1 beta is deployed at https://shopping.vvivid.dev. Browser sign-in/refresh, Codex and ChatGPT MCP reads/writes, and live restart persistence have been verified. The active Example Walmart list contains three sourced, unchecked sample groceries; no purchase or spending was invented. Current build: `0.1.0-beta.2` / application `7970b5c`. Physical-phone and real-receipt checks remain beta follow-up. See [RESUME.md](RESUME.md) for current evidence and limits.

A mobile shopping list managed primarily by AI agents through built-in MCP. Open the list at Walmart, identify the right product, and check it off. Agents maintain product details, lists, purchase history, and preferences; the server computes insights.

Status: October 19, 2026. The V1 beta is deployed. Browser sign-in and refresh, both MCP clients, sourced example creation and live restart persistence have passed. CP01/02/04/05/07/08/09 are locally complete with independent approval. CP03/06/10 retain the remaining phone, real-receipt and live security acceptance checks; those checks are not claimed complete. Automatic migration backups are removed and the correction is independently approved. See CHECKPOINTS.md for the evidence ledger.

The Go application serves four compact mobile views, a narrow browser API and16 built-in MCP tools over shared owner/scope-checked services. Agents maintain sourced purchase drafts, confirmation and full void/replacement corrections; History uses immutable confirmed snapshots and Insights computes recorded spending/compatible quantities. Complete Keycloak configuration enables sign-in, encrypted durable sessions and bearer authentication. Accounts and personal data are never automatically seeded. Readiness checks configured identity and live persistence/migrations; the unconfigured shell stays503. See [docs/DOMAIN.md](docs/DOMAIN.md) and the [release and persistence runbook](docs/RUNBOOK.md).

## Start or resume

1. Read `AGENTS.md`.
2. Read `RESUME.md` for the actual current state and next action.
3. Read `REQUIREMENTS.md`, `SPEC.md`, and `ARCHITECTURE.md`.
4. Work through `CHECKPOINTS.md` from the first incomplete checkpoint.

## Selected stack

Go + `html/template` + Tailwind CSS + minimal JavaScript + SQLite. One Go application serves the mobile site, Web API, and MCP endpoint. Reuse existing Traefik and Keycloak. **No separate MCP gateway.**

## Documents

| File | Purpose |
| --- | --- |
| REQUIREMENTS.md | User requirements, scope, and acceptance mapping |
| SPEC.md | Domain model, API routes, MCP tools, scopes, and behavior |
| ARCHITECTURE.md | Runtime layout, persistence, security, deployment and restart design |
| CHECKPOINTS.md | Ordered milestones, exit criteria, and evidence ledger |
| RESUME.md | New-chat entry point and current progress |
| docs/KEYCLOAK.md | Prior read-only deployment findings and integration constraints |
| docs/AUTH.md | Identity configuration, local enrollment, rotation and live acceptance |
| docs/RUNBOOK.md | Immutable image, prepared Compose/Traefik, restart tests and server-only persistence policy |
| docs/DECISIONS.md | Selected choices and unresolved decisions |
| reference/ui-demo/ | Accepted React UI demo for visual reference only |
| reference/screenshots/ | Design snapshots from this chat |

The local project documents are canonical for development. Reference Page: https://chatgpt.com/space/page_003f65d178688191a06677d005a807c3

## Local development

Use Go **1.26.9** and Node.js **24** with npm. The repository is [VVivid/AgentShoppingList](https://github.com/VVivid/AgentShoppingList). The Go module retains the local name `agentshoppinglist`; no module-path migration is required to build it. Exact dependency selections and the migration plan are in [docs/DEPENDENCIES.md](docs/DEPENDENCIES.md).

```sh
git clone git@github.com:VVivid/AgentShoppingList.git
cd AgentShoppingList
npm ci --ignore-scripts
make assets
make check
make build
./bin/shopping
```

Open `http://127.0.0.1:8090`. The four pages use Go `html/template` and the CSS compiled from `web/styles/app.css`. Templates and compiled CSS are embedded in the binary; Node is used only for asset builds and local checks. `reference/ui-demo` is excluded from asset scanning and serving.

`make check` requires Node.js 24 and runs the guest browser-script regressions with its built-in test runner, followed by Go vet/tests. The script tests need no npm dependencies. `make run` starts the same application with `go run`. `make fmt` formats Go sources. `npm run watch:css` watches CSS/templates while developing; restart the Go process to embed the result. The compiled CSS is tracked, so a Go-only build can render the current shell without npm installed. Rebuild CSS whenever its source/templates change.

Optional environment settings (the application does not load `.env` files):

| Setting | Default | Validation |
| --- | --- | --- |
| `SHOPPING_HTTP_ADDR` | `127.0.0.1:8090` | IP address, `localhost`, or empty host followed by port 1–65535; `:8090` binds all interfaces |
| `SHOPPING_SHUTDOWN_TIMEOUT` | `10s` | Go duration greater than zero and at most `1m` |
| `SHOPPING_DB_PATH` | `data/shopping.sqlite` | Persistent file path; creates private parent directories; rejects memory-only database |
| `SHOPPING_DB_BUSY_TIMEOUT` | `2s` | Go duration between `1ms` and `10s` |

The default development listener is local. The deployed beta uses `https://shopping.vvivid.dev`. Invalid settings fail before listening. SIGINT/SIGTERM allows active requests to finish within the shutdown timeout. Logs exclude credentials, request bodies and query strings. `make build` also builds the operator-only `shopping-admin` enrollment/disable command and existing manual tools. `shopping version` and `shopping-admin version` identify the running build. See [docs/AUTH.md](docs/AUTH.md) for the optional complete identity bundle and explicit enrollment inputs.

| Route | Current behavior |
| --- | --- |
| `/`, `/products`, `/history`, `/insights` | Authenticated list/catalog, confirmed purchase history and recorded-purchase charts; sign-in/out when identity is configured |
| `/static/app.css`, `/static/app.js` | Embedded compiled CSS and small browser interaction script |
| `/health/live` | 200 while HTTP is serving |
| `/health/ready` | 200 for configured identity plus available DB/current complete migrations; otherwise503 |
| `/auth/login`, `/auth/callback`, `/auth/logout` | Configured-only OIDC+PKCE and durable CSRF-protected sessions |
| `/.well-known/oauth-protected-resource/mcp`, `.../api/v1` | Configured-only exact resource metadata |
| `/api/v1/*`, `/mcp` | Configured-only shopping adapters; see DOMAIN.md for current routes/tools and validation |

SQLite uses WAL, one writer, a bounded read-only pool and per-connection foreign keys. Embedded migrations reject altered/newer/gapped history and existing schemas without application history. No accounts are seeded; identity lookup never enrolls automatically. Fresh startup, enrollment and supported upgrades create no backup archive or backup scratch directory. Keep shopping data and runtime secrets on the server: no backups, snapshots, off-server copies, schedules or Mac recovery keys. Existing manual backup tools require a future explicit request and are not invoked by application paths. See [docs/RUNBOOK.md](docs/RUNBOOK.md). Data files are Git-ignored. Restricted local validation can use `GOMODCACHE=$PWD/.cache/go-mod GOCACHE=$PWD/.cache/go-build make check build`.

The deployed beta enrolls only the owner and has separate Codex and ChatGPT OAuth clients. Owner preferences are America/New_York, USD, Walmart, Monday week start; these are user selections, not global defaults. A new deployment must provision its own private secrets, exact callbacks and explicitly enrolled subject; the repository contains no live credentials or database. Follow [CHECKPOINTS.md](CHECKPOINTS.md) and [RESUME.md](RESUME.md) for verification and next work.
