# Architecture

`MCP connection -> dedicated database pool -> loopback endpoint -> SSH profile` is represented directly in `internal/manager`. A tunnel is keyed by SSH profile, so every configured endpoint using that profile shares one SSH transport while retaining a separate loopback listener, pool, and credential set.

Each profile transport owns its SSH client, keepalive loop, reconnect loop, and cancellation context; it owns one loopback-only listener per configured endpoint. Reconnect is single-flight because only its owning loop attempts connections; delays exponentially back off with jitter. While a profile is unavailable, dependent pools cannot be acquired. A changed transport generation causes a dependent pool to close and recover lazily before it can be reused.

The server exposes status only; it never exposes a shell, destination overrides, key paths, or credentials. Connection status performs a bounded `SELECT version()` health check.

The SQL policy lexes statements and permits only a deliberately narrow subset of `SELECT`, `SHOW`, and non-`ANALYZE` `EXPLAIN`; every query is also placed in a read-only transaction. Current limitation: forwarding connections already in flight may fail on an SSH disconnect. The server deliberately cannot determine whether the database executed such a query and will not retry it.
