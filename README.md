# sahlink

Test and debug utility for a program **this tool starts**. It records stdin, stdout, and stderr. An optional relay listens on a loopback address and dials one upstream host:port. A control socket can read those streams and write to stdin and to the relay.

**sah** means Service Automation Hub. The hub encompasses the platform and the suite of utilities.

sahlink does not attach to an existing PID and does not ptrace. It does not see sockets the child opens on its own.

Version 0.2.0 also forwards TCP, the local half of `ssh -L`. `sahlink web1:5432` listens on `127.0.0.1` and the host's agent dials `127.0.0.1:5432`. `sahlink rdp win1` prints the mstsc, FreeRDP, and macOS Remote Desktop command lines. `-R` exits 2. See `docs/reverse-forward.md` and `docs/recorded-rdp-gateway.md`.

```bash
go install ./cmd/sahlink
sahlink run -- /bin/echo ok
```

Stderr prints `socket=` and `token_file=`. The token is not printed. Delete the session directory when you are finished. Captured output can contain secrets the child prints.

`internal/client`, `internal/config`, `internal/token`, and `internal/secfile` are copied from sahconnect and must not be weakened. Login stays `sahconnect login`.

Usage: `docs/usage.md`. Suite contract: `~/development/systemapi.io/docs/sah-suite.md`. The TCP contract is `~/development/systemapi.io/docs/sah-tcp-protocol.md` on `feat/sah-channels` (not confirmed on `main`).

## Next

`go test ./...`
