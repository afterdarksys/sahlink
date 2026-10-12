# sahlink usage

Module `github.com/afterdarksys/sahlink`. Version 0.1.0.

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

## Finish

1. Stop the child. sahlink signals the process group. Delete the session directory so the token file and the capture are gone. Done when the directory is gone.

## Next

`go test ./...`
