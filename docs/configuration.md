# Configuration reference

`db-mcp` reads one YAML configuration file at startup:

```sh
db-mcp -config /etc/db-mcp/config.yaml
```

Relative paths in the file are resolved relative to the configuration file itself. Passwords, SSH private keys, and passphrases do not belong in YAML or MCP tool arguments.

## Complete example

```yaml
version: 1

defaults:
  readonly: true
  connect_timeout: 10s
  query_timeout: 30s
  max_rows: 500
  max_result_bytes: 4194304
  max_concurrent_queries: 4

ssh_profiles:
  production-bastion:
    host: bastion.example.internal
    port: 22
    username: ubuntu
    private_key_file: secrets/production-bastion.pem
    # private_key_passphrase_file: secrets/production-bastion-passphrase
    host_key_policy: known_hosts
    known_hosts_file: known_hosts
    keepalive_interval: 30s
    keepalive_failures: 3
    reconnect:
      enabled: true
      initial_delay: 1s
      max_delay: 30s

connections:
  - id: orders-production
    driver: mysql
    host: orders-db.internal
    port: 3306
    database: orders
    username: db_mcp_reader
    password_file: secrets/orders-production-password
    ssh_profile: production-bastion
    readonly: true
    query_timeout: 20s

  - id: analytics-local
    driver: postgres
    host: 127.0.0.1
    port: 5432
    database: analytics
    username: db_mcp_reader
    password_env: DB_MCP_ANALYTICS_PASSWORD
    readonly: true
```

## Root fields

| Field | Required | Meaning |
| --- | --- | --- |
| `version` | Yes | Must be `1`. |
| `defaults` | No | Default limits inherited by connections that omit them. |
| `ssh_profiles` | No | Named, reusable SSH transport profiles. |
| `connections` | Yes | One or more named database connections. |

## `defaults`

| Field | Default when omitted | Meaning |
| --- | --- | --- |
| `readonly` | `true` | May not be `false`; read-only mode is mandatory. |
| `connect_timeout` | Driver default | Reserved per-connection connection timeout. It is validated (values above five minutes are rejected), but current drivers use their built-in 10-second connection timeout. |
| `query_timeout` | `30s` | Deadline for one database operation. Values above 24 hours are rejected. |
| `max_rows` | `500` | Maximum rows returned by `db_query`. |
| `max_result_bytes` | `4194304` | Maximum JSON-encoded result payload size (4 MiB). |
| `max_concurrent_queries` | `4` | Per-connection query admission limit. |

Durations use Go syntax, such as `500ms`, `10s`, or `2m`.

## `ssh_profiles.<name>`

SSH profiles own long-lived SSH transports. Connections naming the same profile share that transport but retain separate loopback forwards, credentials, and database pools.

| Field | Required | Meaning |
| --- | --- | --- |
| `host` | Yes | Bastion DNS name or IP address. |
| `port` | No | SSH port; defaults to `22`. |
| `username` | Yes | SSH login name. |
| `private_key_file` | Yes | Private key file, normally mode `0600`. |
| `private_key_passphrase_file` | No | File containing an encrypted-key passphrase. Never include the passphrase in YAML. |
| `host_key_policy` | Yes | Must be `known_hosts`. Insecure trust-on-first-use is not supported. |
| `known_hosts_file` | Yes | OpenSSH `known_hosts` file used to reject unknown/changed hosts. |
| `keepalive_interval` | No | SSH keepalive period; defaults to `30s`. |
| `keepalive_failures` | No | Consecutive failed keepalives before reconnect; defaults to `3`. |
| `reconnect.enabled` | No | Enables bounded reconnect attempts. |
| `reconnect.initial_delay` | No | Initial retry delay; defaults to `1s`. |
| `reconnect.max_delay` | No | Backoff ceiling; defaults to `30s`. |

Enroll host keys out of band, for example with `ssh-keyscan`, and independently verify the resulting fingerprint before using it:

```sh
ssh-keyscan -H bastion.example.internal >> /etc/db-mcp/known_hosts
chmod 600 /etc/db-mcp/known_hosts
```

## `connections[]`

Every connection has a unique `id`. MCP tools require this ID explicitly; there is no default connection.

| Field | Required | Meaning |
| --- | --- | --- |
| `id` | Yes | Unique stable connection identifier. |
| `driver` | Yes | `postgres` or `mysql`. |
| `host` / `port` | Yes | Database endpoint. With `ssh_profile`, this endpoint is resolved from the bastion. |
| `database` | Yes | Database name. |
| `username` | Yes | Least-privilege database account name. |
| `password_file` | Exactly one secret source | File containing only the password, ending with an optional newline. Must normally be mode `0600`. |
| `password_env` | Exactly one secret source | Name of an environment variable containing the password. It must be a valid environment-variable name. |
| `ssh_profile` | No | Name of a configured SSH profile. Omit for a direct database connection. |
| `readonly` | No | May be `true` or omitted. `false` is rejected. |
| `connect_timeout`, `query_timeout`, `max_rows`, `max_result_bytes`, `max_concurrent_queries` | No | Per-connection override of `defaults`. |

`password_file` and `password_env` are mutually exclusive. Use a service manager secret facility or a narrowly scoped environment variable when using `password_env`; do not place a password in a shell profile or repository file.

## Required file permissions

On Unix-like systems, the server rejects a private key, key-passphrase file, or password file that grants group or other access. Recommended layout:

```sh
sudo install -d -m 0700 /etc/db-mcp/secrets
sudo chown -R dbmcp:dbmcp /etc/db-mcp
sudo chmod 600 /etc/db-mcp/secrets/* /etc/db-mcp/known_hosts
```

The server validates file existence during startup. It does not connect to databases until a tool requires the configured connection.

## Validation and current limits

The configuration loader rejects duplicate IDs, unsupported drivers, invalid ports, unknown SSH profiles, missing or insecure secret files, invalid limits, and any attempt to disable read-only mode. Database TLS options and external secret-manager commands are not configuration features yet; use the protected password-file or environment source described above.
