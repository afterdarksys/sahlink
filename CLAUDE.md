# CLAUDE.md

Read `AGENTS.md`. It wins if this file disagrees.

sahlink starts a program and records its stdio. It does not attach to a PID. Version 0.2.0 also forwards TCP to an agent and can print RDP connect lines. It does not open a reverse listener and it does not record TCP bytes. The suite contract is `~/development/systemapi.io/docs/sah-suite.md`. The TCP contract is `~/development/systemapi.io/docs/sah-tcp-protocol.md`.

## Next

`go test ./...`
