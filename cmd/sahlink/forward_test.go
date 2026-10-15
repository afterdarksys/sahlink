package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	fn()
	_ = w.Close()
	os.Stderr = old
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String()
}

func TestReverseUnsupported(t *testing.T) {
	var code int
	out := captureStderr(t, func() {
		code = run([]string{"-R", "15432", "web1:80"})
	})
	if code != 2 || !strings.Contains(out, "reverse_unsupported") {
		t.Fatalf("code %d out %q", code, out)
	}
	out = captureStderr(t, func() {
		code = run([]string{"--reverse", "web1:22"})
	})
	if code != 2 || !strings.Contains(out, "reverse_unsupported") {
		t.Fatalf("code %d out %q", code, out)
	}
}

func TestParseForward(t *testing.T) {
	spec, code := parseForward([]string{"-L", "15432", "web1:5432"}, false)
	if code != 0 || !spec.hasLocal || spec.localPort != 15432 || spec.agent != "web1" || spec.destHost != "local" || spec.destPort != 5432 {
		t.Fatalf("%+v %d", spec, code)
	}
	spec, code = parseForward([]string{"--via", "gw1", "10.0.0.7:3389"}, false)
	if code != 0 || spec.agent != "gw1" || spec.destHost != "10.0.0.7" || spec.destPort != 3389 {
		t.Fatalf("%+v %d", spec, code)
	}
	spec, code = parseForward([]string{"--via", "gw1", "[2001:db8::1]:3389"}, false)
	if code != 0 || spec.destHost != "2001:db8::1" || spec.destPort != 3389 || spec.agent != "gw1" {
		t.Fatalf("%+v %d", spec, code)
	}
	spec, code = parseForward([]string{"10.0.0.7:3389"}, false)
	if code != 0 || spec.agent != "10.0.0.7" || spec.destHost != "local" || spec.destPort != 3389 {
		t.Fatalf("ip without --via %+v", spec)
	}
	_, code = parseForward([]string{"-L", "0.0.0.0", "web1:80"}, false)
	if code != 2 {
		t.Fatalf("bind %d", code)
	}
	_, code = parseForward([]string{"-L", "::", "web1:80"}, false)
	if code != 2 {
		t.Fatalf("v6 bind %d", code)
	}
	spec, code = parseForward([]string{"win1"}, true)
	if code != 0 || spec.agent != "win1" || spec.destHost != "local" || spec.destPort != 3389 {
		t.Fatalf("rdp %+v %d", spec, code)
	}
}
