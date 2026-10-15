package rdp

import (
	"errors"
	"runtime"
	"strings"
	"testing"
)

func TestLinesKeepPercent(t *testing.T) {
	lines := Lines(15432)
	if len(lines) != 3 {
		t.Fatal(lines)
	}
	if lines[0] != "mstsc /v:127.0.0.1:15432" {
		t.Fatal(lines[0])
	}
	if lines[1] != "xfreerdp /v:127.0.0.1:15432 /cert:tofu" {
		t.Fatal(lines[1])
	}
	if !strings.Contains(lines[2], "rdp://full%20address=s:127.0.0.1:15432") {
		t.Fatal(lines[2])
	}
	if strings.Contains(lines[2], " ") && !strings.HasPrefix(lines[2], "open ") {
		t.Fatal(lines[2])
	}
}

func TestLaunchDoesNotWait(t *testing.T) {
	var name string
	var args []string
	err := Launch(3389, func(n string, a ...string) error {
		name = n
		args = append([]string(nil), a...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	switch runtime.GOOS {
	case "darwin":
		if name != "open" || len(args) != 1 || args[0] != URL(3389) {
			t.Fatalf("%s %v", name, args)
		}
	case "windows":
		if name != "mstsc" {
			t.Fatal(name)
		}
	default:
		if name != "xfreerdp" {
			t.Fatal(name)
		}
	}
	err = Launch(3389, func(string, ...string) error { return errors.New("missing") })
	if !errors.Is(err, ErrMissing) {
		t.Fatal(err)
	}
}
