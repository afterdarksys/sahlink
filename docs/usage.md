# sahlink usage

Module `github.com/afterdarksys/sahlink`. Version 0.2.0. `run` and `io` are unchanged from 0.1.0.

## Run a program

1. `sahlink run -- COMMAND...` starts `COMMAND`. Done when stderr prints `socket=` and `token_file=` and the command's exit code is the exit code of `COMMAND`.
2. The session directory is a new `0700` directory unless you pass `--dir`. `--dir` must already exist and be mode `0700`. sahlink does not chmod a directory you pass, so it will not chmod `/tmp`.
3. The control socket is `control.sock` in that directory, mode `0600`. The token file is `token`, mode `0600`, with no trailing newline. The token is 32 random bytes, base64 raw URL encoding. It is not printed and it is not placed in the environment.
4. Stdout and stderr of the child are mirrored to the sahlink process and stored. Default capture is 1 MiB per stream. The ceiling is 32 MiB (`--max-bytes`). Older bytes drop. A read below the retained base returns `dropped`.

`--pid`, `--attach`, and `--ptrace` print `attach_unsupported` and exit 2.

The child inherits the operator environment. sahlink strips inherited `SAHLINK_TOKEN`, `SAHLINK_SOCKET`, `SAHLINK_TOKEN_FILE`, and `SAHLINK_PROXY`, then sets:

- `SAHLINK_SOCKET` — control socket path
- `SAHLINK_TOKEN_FILE` — token path, not the token
- `SAHLINK_PROXY` — loopback listen address, only when the relay is on

## Read and write

1. `sahlink io --socket PATH --token-file PATH status`
2. `sahlink io --socket PATH --token-file PATH read STREAM` writes the raw bytes of `STREAM` to stdout. The read starts at offset 0 and returns at most 64 KiB.
3. `sahlink io --socket PATH --token-file PATH write STREAM` reads stdin, at most 64 KiB, and writes it to `STREAM`.

Read streams: `stdin`, `stdout`, `stderr`, `socket-rx`, `socket-tx`.

Write streams: `stdin`, `socket-rx` (toward the upstream), `socket-tx` (toward the dialer). A write to `stdout` or `stderr` is `bad_stream`. Those streams are records.

The token is read from the file. It is not an argument. A symlink, a non-regular file, group or world bits, a newline, or a length outside 16..512 is `token_file_rejected`.

## Optional relay

Pass both `--proxy-listen 127.0.0.1:0` and `--proxy-upstream HOST:PORT`. One without the other is `proxy_incomplete`.

The listen address must be a loopback IP. The name `localhost` and `0.0.0.0` are refused. The upstream is one host:port. Link-local, unspecified, multicast, `metadata.google.internal`, `metadata.goog`, and `fd00:ec2::254` are refused. A DNS name is resolved and every address must pass that check. The check runs again before dial.

The relay does not intercept connections the child opens itself. Point the child at `SAHLINK_PROXY` if you want it to use the relay.

## Forward a TCP port

The forward uses the SystemAPI TCP channel (`sah-tcp-protocol.md` on `feat/sah-channels`). Login is `sahconnect login`. This tool reads that token file. A token bound to another origin is never sent.

1. `sahlink web1:5432` prints `127.0.0.1:<port>` and forwards each accepted connection to `web1`'s `127.0.0.1:5432`. The target host in the ticket is `local`.
2. `sahlink -L 15432 web1:5432` binds that port on `127.0.0.1`. `-L` is an integer port. A host in `-L` is `bind_refused`.
3. `sahlink --via gw1 10.0.0.7:3389` resolves agent `gw1`. The target is the address `10.0.0.7` and port 3389.
4. `sahlink 10.0.0.7:3389` without `--via` treats `10.0.0.7` as the agent host name and the target as `local`. An IPv6 target is only `[addr]:port`, and only with `--via`. A zone id is `bad_target`.
5. `sahlink win1:3389` and `sahlink rdp win1` also print the mstsc, xfreerdp, and macOS `open` lines. `--port` changes the remote port. `--launch` starts the platform client. If that client is missing, the forward stays up and stderr prints `rdp_client_missing`.
6. `-R`, `--remote`, and `--reverse` print `reverse_unsupported` and exit 2. See `docs/reverse-forward.md`.

The listener is `127.0.0.1` only. There is no flag for `0.0.0.0` or `::`. Ctrl-C closes every channel. One connection prints `tcp_close reason=... bytes_in=... bytes_out=...` on stderr. The process exits non-zero if any connection failed. TCP bytes are not recorded. `docs/recorded-rdp-gateway.md` is a design note, not a gateway.

`internal/client`, `internal/config`, `internal/token`, and `internal/secfile` are copied from sahconnect. Do not weaken them.

## Finish

1. Stop the child. sahlink signals the process group. Delete the session directory so the token file and the capture are gone. Done when the directory is gone.

## Next

`go test ./...`
