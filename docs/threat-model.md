# Threat model

The primary risks are SSH man-in-the-middle attacks, accidental public exposure of local forwards, credential disclosure, and write-capable queries. Known-host verification is required, listeners are loopback-only and ephemeral, and passwords/key passphrases come only from protected files or explicitly named environment variables. Configuration rejects `readonly: false`.

Operators must restrict secret/key files (normally mode `0600`) and database users must have read-only grants. The conservative SQL policy and read-only transactions are defense in depth, not a replacement for least-privilege database roles. TLS from the MCP host to the SSH bastion is SSH; configure database-level TLS separately if required by your security boundary.
