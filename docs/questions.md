# Questions and locked client choices

The client follows `~/development/systemapi.io/docs/sah-tcp-protocol.md` on branch `feat/sah-channels`. That branch is not confirmed on `main`. Nothing here invents server behavior.

- Status and close frames use a nested `payload` object, as the examples in the contract do.
- `sahlink 10.0.0.7:3389` without `--via` treats `10.0.0.7` as the agent host name and the target host as `local`. An IP target requires `--via`.
- IPv6 targets are only accepted as `[addr]:port` with `--via`. A zone id (`%eth0`) is `bad_target`.
- There is no flag that binds anything but `127.0.0.1`. `0.0.0.0` and `::` are `bind_refused`.
- The client refuses the always-refused address classes before it asks for a ticket. `target_denied` for a routable address is the server's policy answer.
- Reverse forwards have no contract frame. See `docs/reverse-forward.md`.
- The client prints `tcp_close` byte counts. It does not write the server audit chain.
- TCP bytes are not recorded. A recorded RDP gateway is design-only. See `docs/recorded-rdp-gateway.md`.
- Listing this call in `sah-suite.md` Wired join is the server contract owner's change. This repo does not edit `systemapi.io`.
- `internal/client`, `internal/config`, `internal/token`, and `internal/secfile` are copied from sahconnect. Do not weaken them. Login stays `sahconnect login`.
