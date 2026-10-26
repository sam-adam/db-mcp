# Read-only database MCP server

A stdio MCP server for PostgreSQL and MySQL with optional, first-class SSH local forwarding. SSH is entirely in-process: it does not spawn `ssh` or require `autossh`.

## Run

```sh
go run ./cmd/db-mcp -config /secure/path/config.yaml
```

Use [config.example.yaml](config.example.yaml) as the configuration reference. Credential, private-key, passphrase, and known-host files are external to YAML. `private_key_passphrase_file` is optional; its content is never logged or returned.

The server implements `initialize`, `tools/list`, and `tools/call` over newline-delimited JSON-RPC on stdin/stdout. All database-facing tools require an explicit `connection_id`; there is no default connection. In addition to query, status, and tunnel tools it exposes read-only connection and schema inspection tools.

`db_query` accepts only a conservative subset of `SELECT`, `SHOW`, and non-`ANALYZE` `EXPLAIN`. Complex CTEs, unknown functions, comments, semicolons, locking clauses, and all write/DDL/admin keywords fail closed. Every query is also run in a read-only transaction. Use database privileges as the final enforcement layer.

## Security model

- Tunnels bind `127.0.0.1:0`, use an unpredictable ephemeral port, and forward only the configured connection endpoint.
- The mandatory `known_hosts` policy rejects unknown or changed SSH host keys. There is no insecure "accept any host key" setting.
- SSH public-key authentication supports encrypted keys via a separate passphrase file. No SSH command execution or caller-selected forwarding exists.
- Each configured database connection owns an independent `sql.DB` pool and credentials. One SSH transport is shared by every endpoint that names the same SSH profile; each endpoint retains its own loopback listener.
- Tunnel loss closes and recreates the affected pool on the next use. Failed statements are never replayed; loss during a query is reported as an uncertain outcome.

See [docs/architecture.md](docs/architecture.md), [docs/threat-model.md](docs/threat-model.md), [docs/troubleshooting.md](docs/troubleshooting.md), and [docs/operations.md](docs/operations.md).
