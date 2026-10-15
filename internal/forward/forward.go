// Package forward is the sahlink TCP client for sah-tcp-protocol.md.
//
// Threats: each accepted local connection is a pivot through the agent.
// The listener binds 127.0.0.1 only. One WebSocket, one ticket, one target.
// Bytes are not sent before the server says connected, and not after the
// local FIN. Frames stay at or under 64 KiB. The local socket is not read
// while a WebSocket write is in progress, so a stalled server stalls only
// this stream. The client stops at the contract's byte cap, idle timeout
// and max duration. Tickets and file contents are not logged. A tcp_status
// error is returned as its code. Does NOT record bytes, sign leases, or
// open a reverse listener. Reverse forwards are out of the contract.
package forward

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
)

const (
	maxFrame       = 64 << 10
	defaultMax     = 10 << 30
	defaultMaxTime = 28800 * time.Second
	defaultIdle    = 900 * time.Second
	handshakeWait  = 30 * time.Second
)

// errIdle is the cancel cause for the idle timer. A caller cancel does not
// use this cause, so Ctrl-C is not reported as idle_timeout.
var errIdle = errors.New("idle_timeout")

// StatusError is a tcp_status error code or a tcp_close reason other than
// closed. The process prints Error() and exits non-zero.
type StatusError struct{ Code string }

func (e *StatusError) Error() string {
	if e == nil || e.Code == "" {
		return "internal"
	}
	return e.Code
}

// CloseInfo is the tcp_close payload, from the client's point of view.
type CloseInfo struct {
	Reason   string
	BytesIn  int64
	BytesOut int64
}

// Options controls one WebSocket stream. Zero durations and a zero byte cap
// use the contract defaults.
type Options struct {
	MaxBytes    int64
	MaxDuration time.Duration
	Idle        time.Duration
	OnClose     func(CloseInfo)
}

func (o Options) maxBytes() int64 {
	if o.MaxBytes <= 0 {
		return defaultMax
	}
	return o.MaxBytes
}

func (o Options) maxDuration() time.Duration {
	if o.MaxDuration <= 0 {
		return defaultMaxTime
	}
	return o.MaxDuration
}

func (o Options) idle() time.Duration {
	if o.Idle <= 0 {
		return defaultIdle
	}
	return o.Idle
}

// Messenger is the WebSocket surface the pump uses. *websocket.Conn satisfies it.
type Messenger interface {
	Read(context.Context) (websocket.MessageType, []byte, error)
	Write(context.Context, websocket.MessageType, []byte) error
}

// Pump relays one local connection over an already-open messenger.
// It waits for connected before reading local. It returns a *StatusError
// when the server sends tcp_status error or a non-closed tcp_close reason.
func Pump(ctx context.Context, local net.Conn, msg Messenger, opt Options) error {
	if local == nil || msg == nil {
		return &StatusError{Code: "internal"}
	}
	ctx, cancel := context.WithTimeout(ctx, opt.maxDuration())
	defer cancel()
	ctx, stopIdle := context.WithCancelCause(ctx)
	defer stopIdle(nil)
	// local.Read does not watch ctx. Closing the socket is what unblocks it
	// on idle, max duration, tcp_close, and a caller cancel.
	go func() {
		<-ctx.Done()
		_ = local.Close()
	}()
	bumped := make(chan struct{}, 1)
	bump := func() {
		select {
		case bumped <- struct{}{}:
		default:
		}
	}
	go func() {
		timer := time.NewTimer(opt.idle())
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-bumped:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(opt.idle())
			case <-timer.C:
				stopIdle(errIdle)
				return
			}
		}
	}()

	hctx, hcancel := context.WithTimeout(ctx, handshakeWait)
	sessionID, err := waitConnected(hctx, msg)
	hcancel()
	if err != nil {
		return classifyWait(ctx, err)
	}
	if sessionID == "" {
		return &StatusError{Code: "protocol_error"}
	}

	var (
		mu      sync.Mutex
		in      int64
		out     int64
		wg      sync.WaitGroup
		errOnce sync.Once
		ret     error
	)
	setErr := func(e error) {
		if e == nil {
			return
		}
		errOnce.Do(func() { ret = e })
		cancel()
	}
	account := func(dst *int64, n int) error {
		if n < 0 {
			return &StatusError{Code: "protocol_error"}
		}
		mu.Lock()
		defer mu.Unlock()
		sum := in + out
		if sum > opt.maxBytes() || int64(n) > opt.maxBytes()-sum {
			return &StatusError{Code: "byte_cap"}
		}
		*dst += int64(n)
		return nil
	}

	wg.Add(2)
	go func() {
		defer wg.Done()
		buf := make([]byte, maxFrame)
		for {
			n, rerr := local.Read(buf)
			if n > 0 {
				bump()
				if aerr := account(&out, n); aerr != nil {
					setErr(aerr)
					return
				}
				chunk := append([]byte(nil), buf[:n]...)
				if werr := msg.Write(ctx, websocket.MessageBinary, chunk); werr != nil {
					if ctx.Err() != nil {
						return
					}
					setErr(&StatusError{Code: "internal"})
					return
				}
			}
			if rerr != nil {
				if ctx.Err() != nil || errors.Is(rerr, net.ErrClosed) {
					return
				}
				if errors.Is(rerr, io.EOF) {
					if werr := msg.Write(ctx, websocket.MessageText, []byte(`{"type":"tcp_eof"}`)); werr != nil && ctx.Err() == nil {
						setErr(&StatusError{Code: "internal"})
					}
					return
				}
				setErr(&StatusError{Code: "internal"})
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for {
			typ, data, rerr := msg.Read(ctx)
			if rerr != nil {
				if ctx.Err() != nil {
					return
				}
				setErr(&StatusError{Code: "protocol_error"})
				return
			}
			if len(data) > maxFrame {
				setErr(&StatusError{Code: "protocol_error"})
				return
			}
			bump()
			switch typ {
			case websocket.MessageBinary:
				if aerr := account(&in, len(data)); aerr != nil {
					setErr(aerr)
					return
				}
				if len(data) == 0 {
					continue
				}
				chunk := append([]byte(nil), data...)
				if _, werr := local.Write(chunk); werr != nil {
					if ctx.Err() != nil {
						return
					}
					setErr(&StatusError{Code: "internal"})
					return
				}
			case websocket.MessageText:
				done, serr := handleText(data, local, opt)
				if serr != nil {
					setErr(serr)
					return
				}
				if done {
					cancel()
					return
				}
			default:
				setErr(&StatusError{Code: "protocol_error"})
				return
			}
		}
	}()
	wg.Wait()
	if ret != nil {
		return ret
	}
	if errors.Is(context.Cause(ctx), errIdle) {
		return &StatusError{Code: "idle_timeout"}
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &StatusError{Code: "max_duration"}
	}
	return nil
}

func classifyWait(parent context.Context, err error) error {
	if errors.Is(context.Cause(parent), errIdle) {
		return &StatusError{Code: "idle_timeout"}
	}
	if errors.Is(parent.Err(), context.DeadlineExceeded) {
		return &StatusError{Code: "max_duration"}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &StatusError{Code: "connect_timeout"}
	}
	if errors.Is(err, context.Canceled) {
		return err
	}
	return err
}

func waitConnected(ctx context.Context, msg Messenger) (string, error) {
	for {
		typ, data, err := msg.Read(ctx)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				return "", err
			}
			return "", &StatusError{Code: "protocol_error"}
		}
		if typ != websocket.MessageText || len(data) > maxFrame {
			return "", &StatusError{Code: "protocol_error"}
		}
		var env statusFrame
		if json.Unmarshal(data, &env) != nil || env.Type == "" {
			return "", &StatusError{Code: "protocol_error"}
		}
		switch env.Type {
		case "tcp_status":
			if env.Payload.Error != "" {
				return "", &StatusError{Code: safeCode(env.Payload.Error)}
			}
			switch env.Payload.Status {
			case "connecting":
				continue
			case "connected":
				if !validSessionID(env.Payload.SessionID) {
					return "", &StatusError{Code: "protocol_error"}
				}
				return env.Payload.SessionID, nil
			default:
				return "", &StatusError{Code: "protocol_error"}
			}
		default:
			if !safeType(env.Type) {
				return "", &StatusError{Code: "protocol_error"}
			}
			continue
		}
	}
}

type statusFrame struct {
	Type    string `json:"type"`
	Payload struct {
		Status    string `json:"status"`
		Error     string `json:"error"`
		SessionID string `json:"session_id"`
		Reason    string `json:"reason"`
		BytesIn   int64  `json:"bytes_in"`
		BytesOut  int64  `json:"bytes_out"`
	} `json:"payload"`
}

func handleText(data []byte, local net.Conn, opt Options) (done bool, err error) {
	var env statusFrame
	if json.Unmarshal(data, &env) != nil || env.Type == "" {
		return false, &StatusError{Code: "protocol_error"}
	}
	switch env.Type {
	case "tcp_status":
		if env.Payload.Error != "" {
			return false, &StatusError{Code: safeCode(env.Payload.Error)}
		}
		return false, nil
	case "tcp_eof":
		if cw, ok := local.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
		return false, nil
	case "tcp_close":
		if opt.OnClose != nil {
			opt.OnClose(CloseInfo{
				Reason:   env.Payload.Reason,
				BytesIn:  env.Payload.BytesIn,
				BytesOut: env.Payload.BytesOut,
			})
		}
		if env.Payload.Reason != "closed" && env.Payload.Reason != "" {
			return true, &StatusError{Code: safeCode(env.Payload.Reason)}
		}
		return true, nil
	default:
		if !safeType(env.Type) {
			return false, &StatusError{Code: "protocol_error"}
		}
		return false, nil
	}
}

func validSessionID(s string) bool {
	const p = "tcs_"
	if len(s) < len(p)+16 || len(s) > len(p)+64 || s[:len(p)] != p {
		return false
	}
	for _, c := range s[len(p):] {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-':
		default:
			return false
		}
	}
	return true
}

func safeType(s string) bool {
	if len(s) == 0 || len(s) > 32 {
		return false
	}
	for _, c := range s {
		if c < 'a' || (c > 'z' && c != '_') {
			return false
		}
	}
	return true
}

func safeCode(s string) string {
	if len(s) == 0 || len(s) > 64 {
		return "protocol_error"
	}
	for _, c := range s {
		if c != '_' && (c < 'a' || c > 'z') {
			return "protocol_error"
		}
	}
	return s
}

// Dial opens the WebSocket and pumps. The URL's query holds the ticket, so
// errors returned from here do not include the URL or the ticket.
func Dial(ctx context.Context, local net.Conn, wsURL string, httpClient *http.Client, opt Options) error {
	if httpClient == nil || local == nil || wsURL == "" {
		return &StatusError{Code: "internal"}
	}
	hctx, cancel := context.WithTimeout(ctx, handshakeWait)
	conn, resp, err := websocket.Dial(hctx, wsURL, &websocket.DialOptions{HTTPClient: httpClient})
	cancel()
	if err != nil {
		return dialError(resp, err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(maxFrame)
	return Pump(ctx, local, conn, opt)
}

func dialError(resp *http.Response, err error) error {
	if resp != nil && resp.Body != nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
	}
	if resp != nil && resp.StatusCode != http.StatusSwitchingProtocols {
		return fmt.Errorf("tcp handshake refused (HTTP %d)", resp.StatusCode)
	}
	var cv *tls.CertificateVerificationError
	if errors.As(err, &cv) {
		return errors.New("tcp connection failed: TLS certificate verification failed")
	}
	var rh tls.RecordHeaderError
	if errors.As(err, &rh) {
		return errors.New("tcp connection failed: TLS handshake failed")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.New("tcp connection failed: handshake timed out")
	}
	return errors.New("tcp connection failed")
}
