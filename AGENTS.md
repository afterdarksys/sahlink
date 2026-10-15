# AGENTS.md

sahlink records stdio, and one opt-in loopback relay, for a process it starts. Version 0.2.0 also forwards TCP through the SystemAPI agent channel.

Usage: `docs/usage.md`. Suite contract: `~/development/systemapi.io/docs/sah-suite.md`. TCP contract: `~/development/systemapi.io/docs/sah-tcp-protocol.md` on `feat/sah-channels`.

## Do this

1. Read `docs/usage.md` before changing flags or streams.
2. A change to capture, the control socket, or the relay includes a negative test: wrong token, `--pid` or `--attach`, a metadata or link-local upstream, and a directory that is not mode `0700`. Done when `go test ./...` reports ok.

## Rules

- Start only the command the operator passed after `--`. `--pid`, `--attach`, `--ptrace`, and the commands `attach` and `ptrace` exit 2 with `attach_unsupported`. Attaching would read another process.
- Keep the relay on a loopback IP. Dial only the upstream named at start. Refuse link-local, unspecified, multicast, `metadata.google.internal`, `metadata.goog`, and `fd00:ec2::254`. Resolve DNS and check again before dial. A single check at startup misses a rebinding answer.
- Compare the control token by SHA-256 of both sides, then `subtle.ConstantTimeCompare`. Reject lengths outside 16..512 before the compare. Do not print the token. The token file is mode `0600` and is not a symlink.
- Do not chmod a directory the operator passed. Require mode `0700`. chmod of `/tmp` would lock out the rest of the system.
- Reject writes to `stdout` and `stderr`. Those buffers are the capture.
- Do not put the token value in the child environment. `SAHLINK_TOKEN_FILE` is a path.
- The child inherits the rest of the operator environment on purpose. Do not add a secret from another product into that environment.
- Captured bytes can contain secrets the child printed. Say that in `docs/usage.md` if you change what is stored.
- The TCP listener binds `127.0.0.1` only. Do not add a flag for any other address. `-R` stays `reverse_unsupported` until the TCP contract grows a reverse frame.
- `internal/client`, `internal/config`, `internal/token`, and `internal/secfile` are copied from sahconnect. Do not weaken `DialGuard`, ticket checks, or token-file checks. Do not add a second login. Login stays `sahconnect login`.
- Do not log a ticket or a WebSocket URL. A dial error says `tcp connection failed` or an HTTP status, not the URL.

## Next

`go test ./...`
