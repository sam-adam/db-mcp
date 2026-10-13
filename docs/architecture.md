# Architecture

`MCP connection -> dedicated database pool -> loopback endpoint -> SSH profile` is represented directly in `internal/manager`. A tunnel is keyed by `(ssh profile, configured database host:port)`, so same-profile connections to one endpoint share SSH transport while retaining separate pools and policies.

Each tunnel owns one loopback listener, SSH client, keepalive loop, reconnect loop, and cancellation context. The listener never binds publicly. Reconnect is single-flight because only its owning loop attempts connections; delays exponentially back off with jitter. A changed tunnel generation causes its dependent pool to close and recover lazily.

The server exposes status only; it never exposes a shell, destination overrides, key paths, or credentials. Connection status performs a bounded `SELECT version()` health check.

Current limitation: forwarding connections already in flight may fail on an SSH disconnect. The server deliberately cannot determine whether the database executed such a query and will not retry it.
