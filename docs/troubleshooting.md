# Troubleshooting

If `db_tunnel_status` reports `failed`, verify bastion reachability, the key format, permissions, and the exact host entry in `known_hosts`. A changed host key is intentionally rejected; investigate the rotation before updating `known_hosts`.

If a database status is unreachable while its tunnel is connected, verify that the configured host and port are reachable from the bastion, not from the MCP host. For transient network loss, wait for the reported reconnect backoff; do not restart the process unless the failure is persistent.

Query failures during a disconnect have uncertain outcome. Check the database before manually deciding whether a query is safe to retry.
