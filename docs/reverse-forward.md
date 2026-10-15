# Reverse forwards

`sahlink -R`, `--remote`, and `--reverse` exit 2 with `reverse_unsupported`.

`sah-tcp-protocol.md` §10 makes a reverse forward (ssh -R) future work. No frame, purpose, or scope opens a listener on the host. A request for one is `bad_purpose`. This client does not invent that frame.

When a later contract revision adds it, the forward stays off unless a server policy explicitly allows that host, port, and actor. The listener on the host is the agent's, and the audit row names who opened it. Until that revision exists, there is nothing to build here.

The local forward (`HOST:PORT`, `-L`, `--via`) is the path this version implements. It binds `127.0.0.1` only.
