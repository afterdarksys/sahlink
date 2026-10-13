# sahlink

Test and debug utility for a program **this tool starts**. It records stdin, stdout, and stderr. An optional relay listens on a loopback address and dials one upstream host:port. A control socket can read those streams and write to stdin and to the relay.

**sah** means Service Automation Hub. The hub encompasses the platform and the suite of utilities.

sahlink does not attach to an existing PID and does not ptrace. It does not see sockets the child opens on its own.

```bash
go install ./cmd/sahlink
sahlink run -- /bin/echo ok
```

Stderr prints `socket=` and `token_file=`. The token is not printed. Delete the session directory when you are finished. Captured output can contain secrets the child prints.

Usage: `docs/usage.md`. Suite contract: `~/development/systemapi.io/docs/sah-suite.md`.

## Next

`go test ./...`
