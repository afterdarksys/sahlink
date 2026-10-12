package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestVersionAndAttachRefusal(t *testing.T) {
	if code := run([]string{"version"}); code != 0 {
		t.Fatalf("version %d", code)
	}
	if code := run([]string{"attach", "1"}); code != 2 {
		t.Fatalf("attach %d", code)
	}
	if code := run([]string{"ptrace"}); code != 2 {
		t.Fatalf("ptrace %d", code)
	}
	if code := run([]string{"run", "--pid", "1"}); code != 2 {
		t.Fatalf("run --pid %d", code)
	}
	if code := run([]string{"run", "--attach", "1"}); code != 2 {
		t.Fatalf("run --attach %d", code)
	}
	if code := run([]string{"run"}); code != 2 {
		t.Fatalf("run without command %d", code)
	}
}

func TestRunEcho(t *testing.T) {
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_ = inW.Close()
	oldIn := os.Stdin
	os.Stdin = inR
	t.Cleanup(func() { os.Stdin = oldIn })
	code := run([]string{"run", "--", "/bin/echo", "ok"})
	_ = w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(r)
	if code != 0 {
		t.Fatalf("code %d out %q", code, buf.String())
	}
	if !strings.Contains(buf.String(), "ok") {
		t.Fatalf("out %q", buf.String())
	}
}
