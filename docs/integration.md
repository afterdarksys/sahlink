# sahlink integration

sahlink does not call systemapi, computeapi, or adssh. Other tools can start it around a program they already plan to run. The suite contract is `~/development/systemapi.io/docs/sah-suite.md`.

Use it to record a program you started while preparing that program for SAH. Do not point it at another user's process. The control token is the capability for the streams of this session only.

The relay is an operator-named loopback hop, not a general proxy. Do not add a flag that takes an arbitrary PID.

## Next

`go test ./internal/session/ -count=1`
