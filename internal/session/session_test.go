package session

import (
	"encoding/base64"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sahlink-test-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func startEcho(t *testing.T, dir string, command ...string) *Session {
	t.Helper()
	s, err := Start(Options{
		Command:    command,
		SocketPath: filepath.Join(dir, "control.sock"),
		TokenPath:  filepath.Join(dir, "token"),
		Token:      "test-token-value-0123456789",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = s.Close()
		_, _ = s.Wait()
	})
	return s
}

func dialToken(t *testing.T, s *Session, dir string) *Client {
	t.Helper()
	token, err := ReadTokenFile(filepath.Join(dir, "token"))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		c, err := Dial(s.SocketPath(), token)
		if err == nil {
			t.Cleanup(func() { _ = c.Close() })
			return c
		}
		last = err
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal(last)
	return nil
}

func TestCaptureStdioAndRejectsAttachFlags(t *testing.T) {
	dir := testDir(t)
	s := startEcho(t, dir, "/bin/echo", "hello-sah")
	c := dialToken(t, s, dir)
	deadline := time.Now().Add(2 * time.Second)
	var got string
	for time.Now().Before(deadline) {
		resp, err := c.Read("stdout", 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		if resp["ok"] != true {
			t.Fatalf("read: %#v", resp)
		}
		raw, _ := resp["data"].(string)
		b, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			t.Fatal(err)
		}
		got = string(b)
		if got != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got != "hello-sah\n" {
		t.Fatalf("stdout = %q", got)
	}
	code, _ := s.Wait()
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
}

func TestWriteStdinRoundTrip(t *testing.T) {
	dir := testDir(t)
	s := startEcho(t, dir, "/bin/cat")
	c := dialToken(t, s, dir)
	resp, err := c.Write("stdin", []byte("ping\n"))
	if err != nil {
		t.Fatal(err)
	}
	if resp["ok"] != true {
		t.Fatalf("write: %#v", resp)
	}
	if _, err := c.CloseChildStdin(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	var got string
	for time.Now().Before(deadline) {
		resp, err := c.Read("stdout", 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := resp["data"].(string)
		b, _ := base64.StdEncoding.DecodeString(raw)
		got = string(b)
		if got == "ping\n" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got != "ping\n" {
		t.Fatalf("stdout = %q", got)
	}
	in, err := c.Read("stdin", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := in["data"].(string)
	b, _ := base64.StdEncoding.DecodeString(raw)
	if string(b) != "ping\n" {
		t.Fatalf("stdin capture = %q", b)
	}
}

func TestAuthRejectsWrongEmptyAndOversizedToken(t *testing.T) {
	dir := testDir(t)
	s := startEcho(t, dir, "/bin/sleep", "30")
	if _, err := Dial(s.SocketPath(), "wrong-token-value-0123456789"); err == nil {
		t.Fatal("wrong token was accepted")
	}
	if _, err := Dial(s.SocketPath(), ""); err == nil {
		t.Fatal("empty token was accepted")
	}
	oversized := make([]byte, MaxTokenBytes+1)
	for i := range oversized {
		oversized[i] = 'a'
	}
	if _, err := Dial(s.SocketPath(), string(oversized)); err == nil {
		t.Fatal("oversized token was accepted")
	}
}

func TestWriteToStdoutRejectedAndDroppedOffset(t *testing.T) {
	dir := testDir(t)
	s, err := Start(Options{
		Command:    []string{"/bin/sleep", "30"},
		SocketPath: filepath.Join(dir, "control.sock"),
		Token:      "test-token-value-0123456789",
		TokenPath:  filepath.Join(dir, "token"),
		MaxCapture: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(); _, _ = s.Wait() })
	c := dialToken(t, s, dir)
	resp, err := c.Write("stdout", []byte("nope"))
	if err != nil {
		t.Fatal(err)
	}
	if resp["ok"] == true {
		t.Fatalf("stdout write accepted: %#v", resp)
	}
	if _, err := s.streams["stdout"].Write([]byte("0123456789abcdef")); err != nil {
		t.Fatal(err)
	}
	got, err := s.streams["stdout"].ReadFrom(0, 4)
	if err != ErrDropped {
		t.Fatalf("offset 0 err=%v data=%q", err, got)
	}
}

func TestProxyRelayAndMetadataRejected(t *testing.T) {
	if err := ValidateProxy("127.0.0.1:0", "169.254.169.254:80"); err == nil {
		t.Fatal("link-local upstream accepted")
	}
	if err := ValidateProxy("127.0.0.1:0", "metadata.google.internal:80"); err == nil {
		t.Fatal("metadata upstream accepted")
	}
	if err := ValidateProxy("0.0.0.0:9", "127.0.0.1:9"); err == nil {
		t.Fatal("wildcard listen accepted")
	}
	if err := ValidateProxy("127.0.0.1:1", ""); err == nil {
		t.Fatal("incomplete proxy accepted")
	}

	up, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer up.Close()
	got := make(chan string, 1)
	go func() {
		c, err := up.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		buf := make([]byte, 16)
		n, _ := c.Read(buf)
		_, _ = c.Write(buf[:n])
		got <- string(buf[:n])
	}()

	dir := testDir(t)
	s, err := Start(Options{
		Command:       []string{"/bin/sleep", "30"},
		SocketPath:    filepath.Join(dir, "control.sock"),
		Token:         "test-token-value-0123456789",
		TokenPath:     filepath.Join(dir, "token"),
		ProxyListen:   "127.0.0.1:0",
		ProxyUpstream: up.Addr().String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(); _, _ = s.Wait() })
	conn, err := net.Dial("tcp", s.ProxyAddr())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "ping" {
		t.Fatalf("echo %q", buf)
	}
	select {
	case body := <-got:
		if body != "ping" {
			t.Fatalf("upstream %q", body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("upstream timeout")
	}
	c := dialToken(t, s, dir)
	deadline := time.Now().Add(2 * time.Second)
	var raw string
	for time.Now().Before(deadline) {
		resp, err := c.Read("socket-rx", 0, 16)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ = resp["data"].(string)
		b, _ := base64.StdEncoding.DecodeString(raw)
		if string(b) == "ping" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("socket-rx = %q", raw)
}

func TestLooseSocketDirRejected(t *testing.T) {
	dir := testDir(t)
	loose := filepath.Join(dir, "loose")
	if err := os.Mkdir(loose, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Start(Options{
		Command:    []string{"/bin/echo", "x"},
		SocketPath: filepath.Join(loose, "control.sock"),
		Token:      "test-token-value-0123456789",
	})
	if err != ErrSocket {
		t.Fatalf("err = %v", err)
	}
}

func TestCommandRequired(t *testing.T) {
	_, err := Start(Options{})
	if err != ErrCommand {
		t.Fatalf("err = %v", err)
	}
}

func TestTokenFileModeAndSymlink(t *testing.T) {
	dir := testDir(t)
	path := filepath.Join(dir, "token")
	if err := WriteTokenFile(path, "test-token-value-0123456789"); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadTokenFile(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadTokenFile(path); err == nil {
		t.Fatal("group-readable token was accepted")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadTokenFile(link); err == nil {
		t.Fatal("symlink token was accepted")
	}
	if err := WriteTokenFile(path, "test-token-value-0123456789"); err == nil {
		t.Fatal("existing token file was replaced")
	}
	short := filepath.Join(dir, "short")
	if err := os.WriteFile(short, []byte("tiny\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadTokenFile(short); err == nil {
		t.Fatal("short token file was accepted")
	}
}

func TestGenerateTokenCharset(t *testing.T) {
	tok, err := GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(tok) < MinTokenBytes {
		t.Fatalf("len %d", len(tok))
	}
	for _, r := range tok {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			t.Fatalf("bad rune %q in %q", r, tok)
		}
	}
	if TokenEqual(tok, tok) != true || TokenEqual(tok, tok+"x") {
		t.Fatal("token compare")
	}
}
