# Operations guide

## Host keys and credentials

Enroll a bastion host key out of band, then verify its fingerprint with your infrastructure team before writing it to the configured `known_hosts` file. Never use an accept-any-key callback. Private keys, passphrase files, and password files must be regular files readable only by their owner (`0600` on Unix-like systems).

`password_file` and `password_env` are mutually exclusive. To use a credential manager such as `pass`, retrieve the secret outside this process and inject it into a narrowly scoped environment, for example: `DB_MCP_PROD_PASSWORD="$(pass show infra/db/prod-reader)" db-mcp -config config.yaml`. Do not put the command or a password in YAML.

## MCP registration

Kiro CLI, Codex, and Claude Code can register the server as a stdio MCP process. Point the command to the built `db-mcp` binary and pass `-config /absolute/path/config.yaml`; keep configuration and secret files outside the repository. The server writes JSON-RPC only to stdout and structured diagnostics only to stderr.

## Recovery

An SSH profile owns a single long-lived transport. Each configured database endpoint has an independent loopback listener on that transport and every connection has an independent pool. A lost profile marks dependent connections unavailable, increments its generation when reconnected, and causes those pools to be replaced lazily. A query interrupted by a tunnel failure is never replayed and has an uncertain outcome.

An open SSH socket is not a database health assertion. Keepalives verify that the SSH transport responds; `db_connection_status` additionally runs a bounded `SELECT version()` against the database.

## Releases

`develop` produces CI-tested release-candidate build artifacts. Stable releases are created only by pushing an immutable `v*` tag for a commit already contained in `main`. The release workflow verifies source formatting, modules, vet, race-enabled tests, and builds archives for Linux amd64/arm64 and macOS amd64/arm64. It then publishes the archives and `checksums.txt` to the GitHub Release.

Create the tag only after the release PR is merged into `main`, for example: `git tag -a v0.1.0 -m 'v0.1.0' && git push origin v0.1.0`. The workflow intentionally fails if the tagged commit is not in `main`.

## Feature matrix

| Capability | Status |
| --- | --- |
| PostgreSQL/MySQL direct and SSH routing | Implemented |
| known_hosts verification and encrypted private keys | Implemented |
| Shared SSH profile transport / isolated database pools | Implemented |
| Reconnect backoff, jitter, cancellation | Implemented |
| Read-only transactions and conservative SQL policy | Implemented |
| Password files and environment secret references | Implemented |
| Schema inspection MCP tools | Implemented |
| Dockerized PostgreSQL/MySQL/SSH integration suite | Not yet implemented |
| Database TLS configuration and external secret-manager API | Not yet implemented |

The application policy is deliberate defense in depth. Dedicated database roles with read-only privileges are required.
