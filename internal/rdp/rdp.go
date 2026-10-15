// Package rdp prints the connect lines for a forwarded RDP port and can
// start the platform client. It does not open a session or record one.
//
// Threats: the desktop client runs on the operator's machine and speaks
// RDP to 127.0.0.1 only. A missing client must not tear down the forward.
// The recorded gateway described in docs/recorded-rdp-gateway.md is not
// implemented here. Does NOT store credentials or keystrokes.
package rdp

import (
	"errors"
	"os/exec"
	"runtime"
	"strconv"
)

// Lines are the three connect commands for a local forward. The Microsoft
// Remote Desktop URL is built by concatenation so the %20 stays literal.
func Lines(port int) []string {
	p := strconv.Itoa(port)
	return []string{
		"mstsc /v:127.0.0.1:" + p,
		"xfreerdp /v:127.0.0.1:" + p + " /cert:tofu",
		"open " + URL(port),
	}
}

// URL is the Microsoft Remote Desktop URL for macOS.
func URL(port int) string {
	return "rdp://full%20address=s:127.0.0.1:" + strconv.Itoa(port)
}

// ErrMissing is returned when the platform client cannot be started.
var ErrMissing = errors.New("rdp_client_missing")

// Launch starts the platform RDP client and does not wait for it.
// start is exec.Command(...).Start when nil. A start error is ErrMissing.
func Launch(port int, start func(name string, arg ...string) error) error {
	if port < 1 || port > 65535 {
		return ErrMissing
	}
	if start == nil {
		start = func(name string, arg ...string) error {
			cmd := exec.Command(name, arg...)
			return cmd.Start()
		}
	}
	p := strconv.Itoa(port)
	var err error
	switch runtime.GOOS {
	case "darwin":
		err = start("open", URL(port))
	case "windows":
		err = start("mstsc", "/v:127.0.0.1:"+p)
	default:
		err = start("xfreerdp", "/v:127.0.0.1:"+p, "/cert:tofu")
	}
	if err != nil {
		return ErrMissing
	}
	return nil
}
