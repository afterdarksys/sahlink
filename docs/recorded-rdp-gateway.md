# Recorded RDP gateway (not built)

`sahlink rdp` forwards TCP to the remote port (3389 unless `--port` is set) and prints a connect line. It does not record the RDP session. The bytes are not stored. `--launch` starts the platform client and leaves the forward up if that client is missing.

A later service can sit where Apache Guacamole sits: the operator's browser talks to a gateway, and the gateway is the RDP client. That service is a different program. This repository does not contain it.

## What that service would have to decide

- The RDP stream is plaintext on the far side of the gateway's TLS. The gateway sees keystrokes, the framebuffer, and clipboard files. Recording any of that needs the same consent and retention rules as a terminal recording, and a place to store it that is not the operator's laptop.
- Credentials for the Windows host must not be written into the sahlink command line, a URL, or a log. A gateway that stores them needs a vault and a short lease, not a flag on this CLI.
- The gateway is a new trust boundary. It is not the systemapi-agent, and it must not be given the agent's host credential.
- A policy has to name which actors may open RDP, to which host, and whether the session is recorded. Default is deny.

Until those decisions are written into a contract, the only RDP path is the TCP forward plus the operator's own client:

```
mstsc /v:127.0.0.1:PORT
xfreerdp /v:127.0.0.1:PORT /cert:tofu
open rdp://full%20address=s:127.0.0.1:PORT
```

The `%20` is literal. The URL is concatenated, not passed through a format string.
