package forward

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func randToken(t *testing.T) string {
	t.Helper()
	var b [18]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return "stk_" + base64.RawURLEncoding.EncodeToString(b[:])
}

type frame struct {
	typ  websocket.MessageType
	data []byte
}

type fakeMsg struct {
	in    chan frame
	mu    sync.Mutex
	got   []frame
	block chan struct{}
	cur   atomic.Int32
	max   atomic.Int32
}

func newFake() *fakeMsg {
	return &fakeMsg{in: make(chan frame, 8)}
}

func (f *fakeMsg) push(typ websocket.MessageType, data string) {
	f.in <- frame{typ: typ, data: []byte(data)}
}

func (f *fakeMsg) Read(ctx context.Context) (websocket.MessageType, []byte, error) {
	select {
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	case fr := <-f.in:
		return fr.typ, fr.data, nil
	}
}

func (f *fakeMsg) Write(ctx context.Context, typ websocket.MessageType, data []byte) error {
	n := f.cur.Add(1)
	defer f.cur.Add(-1)
	for {
		old := f.max.Load()
		if n <= old || f.max.CompareAndSwap(old, n) {
			break
		}
	}
	if f.block != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-f.block:
		}
	}
	f.mu.Lock()
	f.got = append(f.got, frame{typ: typ, data: append([]byte(nil), data...)})
	f.mu.Unlock()
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func (f *fakeMsg) writes() []frame {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]frame(nil), f.got...)
	return out
}

func connected() string {
	return `{"type":"tcp_status","payload":{"status":"connected","session_id":"tcs_abcdefghijklmnop","peer":"127.0.0.1:9"}}`
}

func TestNoWriteBeforeConnected(t *testing.T) {
	msg := newFake()
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		_, _ = b.Write([]byte("hi"))
		close(done)
	}()
	errc := make(chan error, 1)
	go func() { errc <- Pump(ctx, a, msg, Options{}) }()
	select {
	case <-done:
		t.Fatal("local write completed before connected")
	case <-time.After(80 * time.Millisecond):
	}
	if len(msg.writes()) != 0 {
		t.Fatal("websocket write before connected")
	}
	msg.push(websocket.MessageText, `{"type":"tcp_status","payload":{"status":"connecting"}}`)
	msg.push(websocket.MessageText, connected())
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("timed out waiting for forward")
	}
	msg.push(websocket.MessageText, `{"type":"tcp_close","payload":{"reason":"closed","bytes_in":0,"bytes_out":2}}`)
	select {
	case err := <-errc:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("pump hung")
	}
	ws := msg.writes()
	if len(ws) == 0 || ws[0].typ != websocket.MessageBinary || string(ws[0].data) != "hi" {
		t.Fatalf("writes %#v", ws)
	}
}

func TestBinaryBeforeConnected(t *testing.T) {
	msg := newFake()
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	msg.push(websocket.MessageBinary, "nope")
	err := Pump(ctx, a, msg, Options{})
	var se *StatusError
	if !errors.As(err, &se) || se.Code != "protocol_error" {
		t.Fatal(err)
	}
	_ = b.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	buf := make([]byte, 8)
	n, _ := b.Read(buf)
	if n != 0 {
		t.Fatalf("wrote %q to local", buf[:n])
	}
}

func TestBackpressureOneWrite(t *testing.T) {
	msg := newFake()
	msg.block = make(chan struct{})
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	msg.push(websocket.MessageText, connected())
	errc := make(chan error, 1)
	go func() { errc <- Pump(ctx, a, msg, Options{}) }()
	if _, err := b.Write([]byte("one")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for msg.cur.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if msg.cur.Load() != 1 {
		t.Fatalf("inflight %d", msg.cur.Load())
	}
	second := make(chan struct{})
	go func() {
		_, _ = b.Write([]byte("two"))
		close(second)
	}()
	select {
	case <-second:
		t.Fatal("second local write completed while the first websocket write was blocked")
	case <-time.After(80 * time.Millisecond):
	}
	if msg.max.Load() != 1 {
		t.Fatalf("max inflight %d", msg.max.Load())
	}
	close(msg.block)
	msg.push(websocket.MessageText, `{"type":"tcp_close","payload":{"reason":"closed","bytes_in":0,"bytes_out":6}}`)
	select {
	case err := <-errc:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestByteCap(t *testing.T) {
	msg := newFake()
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	msg.push(websocket.MessageText, connected())
	errc := make(chan error, 1)
	go func() { errc <- Pump(ctx, a, msg, Options{MaxBytes: 4}) }()
	_, _ = b.Write([]byte("0123456789"))
	select {
	case err := <-errc:
		var se *StatusError
		if !errors.As(err, &se) || se.Code != "byte_cap" {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("timeout")
	}
	if len(msg.writes()) != 0 {
		t.Fatalf("sent %d frames past the cap", len(msg.writes()))
	}
}

func TestMaxDurationAndIdle(t *testing.T) {
	msg := newFake()
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := Pump(ctx, a, msg, Options{MaxDuration: 150 * time.Millisecond})
	var se *StatusError
	if !errors.As(err, &se) || se.Code != "max_duration" {
		t.Fatal(err)
	}

	msg = newFake()
	a, b = net.Pipe()
	defer a.Close()
	defer b.Close()
	msg.push(websocket.MessageText, connected())
	err = Pump(ctx, a, msg, Options{Idle: 150 * time.Millisecond})
	if !errors.As(err, &se) || se.Code != "idle_timeout" {
		t.Fatal(err)
	}
}

func TestStatusAndClose(t *testing.T) {
	msg := newFake()
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	msg.push(websocket.MessageText, `{"type":"tcp_status","payload":{"error":"target_denied"}}`)
	err := Pump(ctx, a, msg, Options{})
	var se *StatusError
	if !errors.As(err, &se) || se.Code != "target_denied" {
		t.Fatal(err)
	}

	// The first Pump closes the local conn on return. Each case needs its own.
	a.Close()
	b.Close()
	a, b = net.Pipe()
	defer a.Close()
	defer b.Close()
	msg = newFake()
	msg.push(websocket.MessageText, connected())
	var got CloseInfo
	msg.push(websocket.MessageText, `{"type":"tcp_close","payload":{"reason":"closed","bytes_in":3,"bytes_out":4}}`)
	err = Pump(ctx, a, msg, Options{OnClose: func(c CloseInfo) { got = c }})
	if err != nil {
		t.Fatal(err)
	}
	if got.Reason != "closed" || got.BytesIn != 3 || got.BytesOut != 4 {
		t.Fatalf("%+v", got)
	}

	a.Close()
	b.Close()
	a, b = net.Pipe()
	defer a.Close()
	defer b.Close()
	msg = newFake()
	msg.push(websocket.MessageText, connected())
	msg.push(websocket.MessageText, `{"type":"tcp_close","payload":{"reason":"reset","bytes_in":1,"bytes_out":1}}`)
	err = Pump(ctx, a, msg, Options{})
	if !errors.As(err, &se) || se.Code != "reset" {
		t.Fatal(err)
	}
}

func TestHalfClose(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, aerr := ln.Accept()
		if aerr == nil {
			accepted <- c
		}
	}()
	client, err := net.Dial("tcp4", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	local := <-accepted
	defer local.Close()
	msg := newFake()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	msg.push(websocket.MessageText, connected())
	msg.push(websocket.MessageText, `{"type":"tcp_eof"}`)
	msg.push(websocket.MessageText, `{"type":"tcp_close","payload":{"reason":"closed","bytes_in":0,"bytes_out":0}}`)
	errc := make(chan error, 1)
	go func() { errc <- Pump(ctx, local, msg, Options{}) }()
	_ = client.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 8)
	_, rerr := client.Read(buf)
	if !errors.Is(rerr, io.EOF) {
		t.Fatalf("read %v", rerr)
	}
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
}

func TestDialOmitsTicket(t *testing.T) {
	secret := randToken(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusUnauthorized)
	}))
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/systems/tcp/web1?ticket=" + secret + "&purpose=tcp:open"
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := Dial(ctx, a, wsURL, srv.Client(), Options{})
	if err == nil {
		t.Fatal("expected dial error")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "ticket=") {
		t.Fatalf("error leaked the ticket: %s", err.Error())
	}
}

func TestCheckBindAddr(t *testing.T) {
	if err := CheckBindAddr("127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"0.0.0.0", "::", "127.0.0.2", "localhost", ""} {
		err := CheckBindAddr(host)
		var se *StatusError
		if !errors.As(err, &se) || se.Code != "bind_refused" {
			t.Fatalf("%s: %v", host, err)
		}
	}
	ln, err := ListenLocal(0)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	host, _, err := net.SplitHostPort(ln.Addr().String())
	if err != nil || host != "127.0.0.1" {
		t.Fatal(ln.Addr())
	}
}

func TestServeOncePerAccept(t *testing.T) {
	ln, err := ListenLocal(0)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var n atomic.Int32
	errc := make(chan error, 1)
	go func() {
		errc <- Serve(ctx, ln, func(context.Context, net.Conn) error {
			n.Add(1)
			return nil
		})
	}()
	for i := 0; i < 2; i++ {
		c, err := net.Dial("tcp4", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		_ = c.Close()
	}
	deadline := time.Now().Add(2 * time.Second)
	for n.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n.Load() != 2 {
		t.Fatalf("opens %d", n.Load())
	}
	cancel()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("serve hung")
	}
}
