# Orchestrator Platform Backend

The Go control plane for Orchestrator (an MDM for clusters of Raspberry Pi / bare-metal
nodes). It exposes **three surfaces**:

1. **Echo HTTP + WebSocket API** — REST for the frontend + a `/ws` push socket for live
   node/state/metrics updates, and a per-node `/api/v1/nodes/:id/terminal` web-SSH socket.
2. **Bidirectional gRPC** for agents — one long-lived stream per node
   (`EstablishStream`): agents send ConnectionInit/Heartbeat/StatusUpdate/SystemInfo/
   MetricsUpdate/CommandStatus/TerminalOutput; the platform sends Commands and Terminal
   frames. See `../GRPC_README.md` and `../proto/orchestrator.proto` (the contract is the
   source of truth; regenerate with `make proto/generate`).
3. **Boot/artifact serving** — `/http-boot/*` (network-first per-serial bootstrap
   serve/withhold decision) and `/cdn/*` (agent packages / OS images). See
   `../docs/NETWORK-FIRST.md`.

> New here? Read `../AGENTS.md` (project conventions + domain map) first. The authoritative HTTP endpoint
> reference is the **Swagger** docs (`make backend/docs` → `backend/docs`) and the
> **Postman** collections in `../postman/` — not the summary in this file.

## Architecture Overview

Clean / hexagonal architecture; the backend is **CQRS** (write ops = Commands under
`application/ports/in/commands/<name>/`, read ops = Queries under
`.../queries/<name>/`, response DTOs = Results under `.../ports/out/results/`).
Dependencies point inward only; the detailed style guide is in `../docs/CODE-STYLE.md`.

- **Domain Layer**: pure business logic + entities (see the domain list under "Models").
- **Application Layer**: use cases, services, port interfaces (commands/queries/results).
- **Infrastructure Layer**: adapters — HTTP (Echo), gRPC server, WebSocket, GORM/Postgres,
  hardware/boot serving. `cmd/server/main.go` is the composition root: it starts the gRPC
  server first (temporary node service), then HTTP/WS, then wires the real services into the
  gRPC layer via setters (`SetNodeService`, `SetNodeCommandService`, …).

## Features

- ✅ **Hexagonal + CQRS**: clean separation; commands/queries/results split
- ✅ **Echo HTTP + WebSocket** and **bidirectional gRPC** (agent stream) surfaces
- ✅ **GORM ORM**: Database operations with PostgreSQL (AutoMigrate on start)
- ✅ **Structured Logging**: Using slog for contextual logging
- ✅ **Configuration Management**: YAML and environment variable support
- ✅ **JWT Authentication**: Token-based authentication
- ✅ **CORS Support**: Configurable cross-origin resource sharing
- ✅ **Graceful Shutdown**: Proper server shutdown handling
- ✅ **Database Migrations**: Auto-migration with GORM
- ✅ **Dependency Injection**: Constructor-based DI pattern

## Project Structure

```
backend/
├── cmd/
│   └── server/
│       └── main.go                    # Application entry point
├── pkg/
│   └── webapp/
│       ├── application/               # Application layer
│       │   ├── app_errors/           # Shared Err* errors
│       │   ├── config/               # Configuration management
│       │   ├── ports/                # Interface definitions
│       │   │   ├── repository/       # Repository interfaces
│       │   │   ├── out/results/      # Response DTOs (Results)
│       │   │   └── in/{commands,queries}/  # CQRS command/query DTOs (one per subdir)
│       │   └── services/             # Application services (one per domain, DI-wired)
│       ├── domain/                    # Domain layer (one package per domain — see Models)
│       └── infrastructure/            # Infrastructure layer (adapters)
│           ├── database/sql/          # GORM models + repositories (FromDomain/ToDomain, Setup())
│           ├── grpc/                  # Bidi gRPC server + generated proto (copy of proto/)
│           ├── crypto/                # AES-256-GCM for secrets-at-rest
│           ├── eepromsigner/          # Client for the eeprom-signer container
│           └── http_server/           # Echo HTTP + WebSocket + boot/cdn/terminal handlers
│               ├── handlers/          # HTTP handlers (auth, node, account, image, ssh_key,
│               │                      #   master_token, organizational_unit, secret,
│               │                      #   provisioning, workflow, httpboot, cdn, terminal, ws)
│               └── middleware/        # JWT auth, etc.
├── go.mod
├── go.sum
├── Makefile
└── README.md
```

## Getting Started

### Prerequisites

- Go — the required version and toolchain are the `go` and `toolchain` lines of `go.mod`;
  read them there, a copy here goes stale at every bump
- PostgreSQL 12 or later
- Make (optional, for using Makefile commands)

### Installation

1. **Clone the repository**
   ```bash
   git clone git@github.com:uptime-lab/orchestrator.git
   cd orchestrator/backend
   ```
   The backend is a directory of the monorepo, not a standalone repository.

2. **Install dependencies**
   ```bash
   make deps
   # or
   go mod tidy
   ```

3. **Set up configuration**
   ```bash
   # The application will use config_local/config.yaml by default
   # You can customize the configuration there or use environment variables
   ```

4. **Set up PostgreSQL database**
   ```bash
   # Option 1: Using Docker Compose (Recommended) — run from the REPO ROOT
   make db/up
   
   # Option 2: Using local PostgreSQL
   createdb orchestrator
   ```
   The database targets live in the root Makefile as `db/up`, `db/down`, `db/logs`,
   `db/shell` — there is no `make db-up` anywhere.

5. **Run the application**
   ```bash
   make run
   # or
   go run cmd/server/main.go
   ```

The server will start on `http://localhost:8080` by default.

## Configuration

The application supports configuration via YAML files with environment variable overrides, read in that order (environment wins).

### Configuration Structure

```
platform/
├── config/
│   └── config.yaml.example          # Example configuration template
├── config_local/
│   └── config.yaml                  # Local development configuration
└── backend/
    └── cmd/server/main.go           # Loads from config_local/config.yaml
```

### Configuration Loading Priority

1. **YAML File**: Loads from `config_local/config.yaml` by default
2. **Environment Variables**: Override YAML values
3. **Default Values**: Fallback if neither YAML nor env vars are set

### Environment Variables

> **This table is a SUBSET and is not authoritative.** The config loader reads roughly a
> hundred environment variables; the fourteen below are the ones that were here when the
> project was a boilerplate. The authoritative sources are, in order:
> **`backend/pkg/webapp/application/config/config.go`** (the loader itself — read the
> `os.Getenv` sites and `getDefaultConfig`), **`config/config.yaml.example`**, and the
> compose file of the install, which is where its live values actually are. Nothing
> security- or quota-relevant is listed here:
> `SECRETS_ENCRYPTION_KEY`, `SERVER_INVITE_CODE`, `STORAGE_TYPE` / `S3_*` / GCP,
> `TERMINAL_ENABLED`, `PROVISIONING_*`, `IMAGE_MAX_*`, `WORKFLOW_*`, the `*_RETENTION_DAYS`
> family and `NODE_FILE_*` are all absent, and configuring an install from this table alone
> produces one that refuses to boot under production validation. `AGENTS.md` points here
> "for the full variable list"; it is not one.

| Variable | Description | Default (code) |
|----------|-------------|---------|
| `CONFIG_PATH` | Path to config file | `../config_local/config.yaml` |
| `APP_ENV` | Application environment | **empty — and empty means PRODUCTION** (see below) |
| `SERVER_HOST` | Server host | `0.0.0.0` |
| `SERVER_PORT` | Server port | `8080` |
| `ENTITLEMENT` / `ENTITLEMENT_FILE` | The signed device-entitlement file (paste form), or a path to a file holding it; empty = the free cap of 25 devices (`docs/ENTITLEMENT.md`) | empty |
| `ENTITLEMENT_SUBSCRIPTION_URL` | Where a device-cap refusal sends the operator | the product page |
| `ACCOUNT_DEVICE_LIMIT` | Admitted devices each account may hold, in addition to the instance cap; 0 = none (`docs/ENTITLEMENT.md`) | 0 |
| `ACCOUNT_DEVICE_LIMIT_CONTACT` | Where an account at its limit is told to write for more devices | empty |
| `DB_HOST` | Database host | `localhost` |
| `DB_PORT` | Database port | `5432` |
| `DB_USER` | Database user | `postgres` |
| `DB_PASSWORD` | Database password | `postgres` |
| `DB_NAME` | Database name | `orchestrator_platform` |
| `DB_SSLMODE` | Database SSL mode | `disable` |
| `JWT_SECRET_KEY` | JWT secret key | `default-secret-key-change-in-production` (rejected in production) |
| `JWT_ACCESS_TOKEN_TTL` | Access token TTL | `15m` |
| `JWT_REFRESH_TOKEN_TTL` | Refresh token TTL | `168h` (7 days) |
| `JWT_ISSUER` | JWT issuer | `orchestrator` |

**`APP_ENV` has no default and its polarity is inverted on purpose.** Only
`development`, `dev`, `local` and `test` opt OUT of fail-closed validation; unset, a typo,
or any other name is treated as **production** and rejects insecure defaults — the built-in
JWT secret, a weak or empty `SECRETS_ENCRYPTION_KEY`, an undeclared plaintext agent channel.
This table used to say the default was `development`, which is the exact bug the inversion
was written to close: a string compare against `"production"` meant a misspelled `APP_ENV`
silently disabled every guard while the server looked healthy, signing tokens with a
publicly known key. See `IsProduction` in `config.go` for the reasoning in full. So if the
backend refuses to start locally with security errors, set `APP_ENV=development` — do not
"fix" the polarity. The three secret checks (`JWT_SECRET_KEY`, `SECRETS_ENCRYPTION_KEY`,
`CDN_SIGNING_KEY`) are the exception: they refuse the boot in every mode, and a local
server that must run on placeholder values says so with `ALLOW_INSECURE_DEV_SECRETS=true`
as well (`backend/.env.example` sets it; production ignores it).

Note also that the loader reads the `DB_*` family and nothing else: a `DATABASE_*` variable
in a compose file is inert, and with `DB_NAME` unset the backend connects to whatever the
YAML file (or, failing that, the code default) names, where AutoMigrate creates an empty
schema. The symptoms are an install that looks empty and a database-password change that
seems to have no effect; the real data is untouched in the other database, so fix the
variable names rather than restoring a backup. Tables in an unexpected database point at a
variable-naming mismatch, not a migration problem.

### YAML Configuration

The application looks for configuration in `config_local/config.yaml` by default. See `config/config.yaml.example` for the complete configuration template.

**Example `config_local/config.yaml`:**
```yaml
server:
  port: 8080
  host: "0.0.0.0"
  cors:
    enabled: true
    allowedOrigins: ["*"]

database:
  host: "localhost"
  port: 5434            # host port published by docker-compose.yml
  user: "postgres"
  password: "postgres"
  dbname: "orchestrator"
  sslmode: "disable"

jwt:
  secret_key: "local-dev-secret"      # any placeholder marker or <32 chars is refused in production
  access_token_ttl: "15m"
  refresh_token_ttl: "168h"
  issuer: "orchestrator"
```

The snippet above is illustrative and covers a fraction of the schema; `config/config.yaml.example`
is the complete template. Environment overrides YAML, and every credential is read from
`<VAR>_FILE` first: a mounted YAML may carry a placeholder secret while the real value
arrives from a secret file, so judge an install's effective configuration from its
environment and secret files, never from the YAML alone.

## Authentication

### Overview
The application supports two authentication methods:
1. **Email/Password** (traditional)
2. **Google / OIDC** (invite-gated; see `docs/GOOGLE-OAUTH-SETUP.md`)

Both methods return JWT tokens for API access. Passkeys (WebAuthn) work as a
passwordless sign-in on top of either, and TOTP as an optional second factor.

### Registration

**Traditional Registration:**
```bash
POST /auth/signup
Content-Type: application/json

{
  "email": "user@example.com",
  "password": "password123",
  "name": "John Doe",
  "account_name": "My Account",
  "invite_code": "optional"
}
```

**Google / OIDC Registration:**
Signup stays gated by the invite code — `OIDC_AUTO_PROVISION` is deliberately off.

### Login

**Email/Password Login:**
```bash
POST /auth/login
Content-Type: application/json

{
  "email": "user@example.com",
  "password": "password123"
}
```

### Token Refresh

```bash
POST /auth/refresh
Content-Type: application/json

{
  "refresh_token": "your_refresh_token"
}
```

### Protected Endpoints

Include JWT token in Authorization header:
```bash
Authorization: Bearer <access_token>
```

### Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `SERVER_INVITE_CODE` | Code for further self-registrations; empty admits the first owner only, then closes sign-up | "" |
| `SERVER_OPEN_REGISTRATION` | `true` admits any new organisation without a code (overrides the invite code; `SERVER_MAX_ACCOUNTS` still applies) | false |
| `APP_BASE_URL` | Public origin of the SPA, used for absolute links in email | "" |
| `EMAIL_ENABLED` | Send outbound email (verification links) | false |
| `EMAIL_SMTP_HOST` | Submission/relay host (Workspace: `smtp-relay.gmail.com`) | "" |
| `EMAIL_SMTP_PORT` | SMTP port; 0 or unset means 587 | 0 (= 587) |
| `EMAIL_SMTP_USERNAME` | Optional; empty for the IP-authorized Workspace relay | "" |
| `EMAIL_SMTP_PASSWORD` | Optional; empty for the IP-authorized Workspace relay. Read from `EMAIL_SMTP_PASSWORD_FILE` first | "" |
| `EMAIL_FROM_ADDRESS` | Sender address; must exist in the sending domain | "" |
| `EMAIL_FROM_NAME` | Sender display name | "" |
| `EMAIL_SMTP_STARTTLS` | Require STARTTLS (production validation enforces it) | false |

Email delivery is optional: with `EMAIL_ENABLED=false` nothing is sent and users
simply stay unverified. A relay needs the sending domain's SPF and DKIM records in
place, and an IP-authorized relay additionally needs the host's egress address
registered with the provider.

## API Endpoints

> **The authoritative reference is Swagger** (`make backend/docs` → generated under
> `backend/docs`, served at `/swagger`) and the **Postman** collections in `../postman/`.
> Keep both in sync when changing handlers (a rule of `docs/CODE-STYLE.md`). The list
> below is an orientation map of the registered route groups, not an exhaustive path list.

**Public:**
- `GET /health` — health check
- `POST /auth/{signup,login,refresh}` — email/password auth → JWT
- `GET /cdn/agent-versions`, `GET /cdn/agent/:version/:arch/:pkgType` — agent packages,
  deliberately open (a device has no JWT when it fetches its own agent; artifacts are
  signature-verified fail-closed by the agent instead)
- `GET /cdn/*` — **HMAC-gated**, not open: the path must carry the `?exp=&sig=` capability
  minted by the image service's `GetSignedURL`, so it is a time-bounded link rather than a
  permanent public one. A bare fetch answers `invalid or expired URL`.
- `GET /http-boot/:serial/{boot.img,boot.sig}` — network-first per-serial bootstrap
  serve/withhold (see `../docs/NETWORK-FIRST.md`)
- `GET /swagger/*` — API docs

**Protected (JWT `Authorization: Bearer <access_token>`), under `/api/v1/`:**
- `users/*` (incl. `GET /users` — list the account's team), `accounts/*` — profile + account CRUD
- `nodes/*` — node CRUD, commands (`POST /nodes/:id/command`, batch `/nodes/commands`),
  files, boot order, OU assignment + `policy`/`effective-policy`, approve/reject,
  `provision-eeprom`, `POST /nodes/:id/reboot-to-recovery` (one-shot netboot with automatic
  boot-target restore), per-user favorites (`GET /nodes/favorites`,
  `PUT /nodes/:nodeId/favorite`); plus `GET /node-commands/:id`
- `organizational-units/*` — 2-level OU tree + inheritable node policies;
  `account-policy` GET/PUT — account-wide policy fallback (applies to unassigned nodes)
- `secrets/*` — account-scoped encrypted secrets (+ attachments)
- `images/*` — OS images; `ssh-keys/*`; `master-tokens/*`
- `provisioning/sd` — mint a master token + stream a ZIP boot bundle
- `workflow-templates/*`, `workflow-executions/*` — scripted multi-node workflows
- `nodes/:id/terminal` — **WebSocket** web-SSH (gated by `TERMINAL_ENABLED`: off in the code default, on in the community compose)

**WebSocket push:** `GET /ws` — live `node_updated` / `node_metrics` / command events to the
frontend (JWT via first message).

## Development

### Available Commands

```bash
# Build the application
make build

# Run the application
make run

# Run tests
make test

# Run tests with coverage
make test-coverage

# Format code
make fmt

# Lint code
make lint

# Clean build artifacts
make clean
```

These are `backend/Makefile` targets; from the repo root prefix them (`make backend/run`,
`make backend/test`, …). There is no `make dev` and no hot-reload target.

## Database

The application uses GORM for database operations with PostgreSQL. Database migrations are handled automatically using GORM's AutoMigrate feature.

### Database Setup

The project includes Docker Compose configuration for easy database setup:

Run these from the **repo root** — the targets are namespaced with a slash, not a dash:

```bash
# Start PostgreSQL database (waits for ready)
make db/up

# Stop database
make db/down

# View database logs
make db/logs

# Connect to database shell (psql)
make db/shell
```

### Database Configuration

For local development your (gitignored) `config_local/config.yaml` is what actually applies
— the built-in code defaults differ and are listed in the environment table above:
- **Host**: localhost
- **Port**: 5434 — the host port `docker-compose.yml` publishes (container 5432), and the
  value `config/config.yaml.example` carries.
- **User**: postgres
- **Password**: postgres
- **Database**: orchestrator

### Domains / models

Each is a full CQRS slice (domain + service + commands/queries/results + GORM repo). GORM
`AutoMigrate` runs each repo's `Setup()` on backend start — new tables/columns appear on deploy,
no manual migration step.

- **account**, **user** — tenancy + auth (email/password JWT + Google/OIDC; master-token flow).
- **node** — managed devices: status, hardware/OS/boot fingerprint, connection state;
  one-shot reboot-to-recovery bookkeeping (`recovery_revert_pending` +
  `recovery_prev_boot_to_os`, restored when the node reconnects netbooted). Auto-generated
  names use the tail of the hardware serial.
- **node_command** — commands sent to agents, with status/progress. A prose list of slugs
  here goes stale; read `domain/node_command` instead — it is also where the queueable set,
  the managed-OS-only set and the per-slug `MinAgentVersion` floors live, none of which a
  prose list conveys.
- **node_file** / **node_tracked_file** — any file on a node can be followed, versioned,
  edited and written back. `node_tracked_files` is the identity (node + path), `node_files`
  the version log with `content` encrypted at rest; path policy (refused vs protected) lives
  in `domain/node_file`.
- **organizational_unit** — 2-level OU tree; inheritable node policies resolve
  node → OU → root → account default → default (tri-state `PolicyFlag` for
  `network_first`/`boot_to_os`, plus inheritable creds/ssh_key/os_image); has `position`
  (ordering) + `parent_id`. The account-wide fallback lives in `account_policies` (one row
  per account, same nullable fields).
- **node_favorite** — per-user favorite nodes (`user_favorite_nodes`), a personal view with
  no policy semantics.
- **secret** — account-scoped secrets (`env|credential|opaque`), **encrypted at rest**
  (AES-256-GCM). A `sensitive` flag splits them: write-only **secrets** return `has_value`
  only, while non-sensitive **variables** (env kind) do return `value`.
  The `sensitive` true→false transition is forbidden; recreate instead. Attachments to OU/node,
  directly or by tag, feed the effective env/secret set.
- **image** — OS images; **ssh_key**; **master_token** — onboarding/adoption tokens.
- **workflow_template** / **workflow_execution** — scripted multi-node workflows.
- Plus transient (not persisted) per-node state: a **metrics** ring buffer (CPU/RAM/load/temp)
  and **terminal** PTY sessions, both relayed over the agent gRPC stream.

## Testing

Run tests with:
```bash
make test
```

Run tests with coverage:
```bash
make test-coverage
```

## Docker

Release images are built by the platform release workflow
(`.github/workflows/platform-release.yml`) on a version tag and published to the container
registry; an install pulls them by tag through `docker-compose.community.yml` — see
[`docs/INSTALL.md`](../docs/INSTALL.md). A local image is `make release/backend` from the
repository root, or `docker build --build-arg GIT_REVISION=$(git rev-parse HEAD) -t <tag> .`
here. The image carries the commit as the `org.opencontainers.image.revision` label, so
`docker inspect` answers which commit is running without a deploy log to keep in step.

## Contributing

1. Fork the repository
2. Create a feature branch
3. Make your changes
4. Add tests for new functionality
5. Ensure all tests pass
6. Submit a pull request

## License

Fair Core License, Version 1.0, ALv2 Future License (`FCL-1.0-ALv2`): the
backend is one of the FCL-licensed directories of the repository, and each
version converts to the Apache License 2.0 two years after it is made
available. The licence map and the full text are in the root `LICENSE`.
