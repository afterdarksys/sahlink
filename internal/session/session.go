// Threats: captures stdin, stdout, stderr, and one opt-in loopback TCP relay
// for a process this package starts. It does not attach to an existing PID
// and it does not ptrace. Possession of the control-socket token lets the
// holder read and write those streams until the session ends. The token is
// not a credential for any other process. Link-local and cloud-metadata
// upstreams are refused. The relay dials only the upstream named at start.
package session

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	DefaultMaxCapture = 1 << 20
	maxCaptureCeiling = 32 << 20
	maxWrite          = 64 << 10
	maxLine           = 256 << 10
	maxCommands       = 1000
)

var (
	ErrCommand = errors.New("command_required")
	ErrSocket  = errors.New("socket_dir_rejected")
	ErrExists  = errors.New("socket_exists")
	ErrDropped = errors.New("dropped")
	ErrClosed  = errors.New("socket_closed")
	ErrLarge   = errors.New("too_large")
	ErrStream  = errors.New("bad_stream")
	ErrRequest = errors.New("bad_request")
	ErrAuth    = errors.New("auth_denied")
)

// Options configures a session. Command is the program to start. An empty
// ProxyListen and ProxyUpstream leaves the relay off.
type Options struct {
	Command       []string
	SocketPath    string
	Token         string
	TokenPath     string
	MaxCapture    int
	MirrorStdout  io.Writer
	MirrorStderr  io.Writer
	ProxyListen   string
	ProxyUpstream string
	Env           []string
}

// Session is one child process plus its control socket.
type Session struct {
	cmd           *exec.Cmd
	stdin         io.WriteCloser
	stdinMu       sync.Mutex
	streams       map[string]*streamBuf
	socketPath    string
	token         string
	tokenPath     string
	proxyAddr     string
	upstream      string
	ln            net.Listener
	proxyLn       net.Listener
	linkMu        sync.Mutex
	clientConn    net.Conn
	upstreamConn  net.Conn
	clientWrite   sync.Mutex
	upstreamWrite sync.Mutex
	closeOnce     sync.Once
	closeErr      error
	waitOnce      sync.Once
	waitErr       error
	exitCode      int
}

type streamBuf struct {
	mu   sync.Mutex
	base int
	data []byte
	max  int
}

func newStream(max int) *streamBuf {
	return &streamBuf{max: max}
}

func (b *streamBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	if len(b.data) > b.max {
		drop := len(b.data) - b.max
		next := make([]byte, b.max)
		copy(next, b.data[drop:])
		b.data = next
		b.base += drop
	}
	return len(p), nil
}

func (b *streamBuf) ReadFrom(offset, max int) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if offset < b.base {
		return nil, ErrDropped
	}
	if offset > b.base+len(b.data) {
		return []byte{}, nil
	}
	start := offset - b.base
	end := len(b.data)
	if max > 0 && start+max < end {
		end = start + max
	}
	out := make([]byte, end-start)
	copy(out, b.data[start:end])
	return out, nil
}

func (b *streamBuf) Len() (base, n int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.base, len(b.data)
}

type request struct {
	Op     string `json:"op"`
	Token  string `json:"token,omitempty"`
	Stream string `json:"stream,omitempty"`
	Offset int    `json:"offset,omitempty"`
	Max    int    `json:"max,omitempty"`
	Data   string `json:"data,omitempty"`
}

// Start launches the child and the control socket. On any setup failure it
// does not leave the child running.
func Start(opts Options) (*Session, error) {
	if len(opts.Command) == 0 || strings.TrimSpace(opts.Command[0]) == "" {
		return nil, ErrCommand
	}
	if err := ValidateProxy(opts.ProxyListen, opts.ProxyUpstream); err != nil {
		return nil, err
	}
	if opts.MaxCapture <= 0 {
		opts.MaxCapture = DefaultMaxCapture
	}
	if opts.MaxCapture > maxCaptureCeiling {
		return nil, ErrLarge
	}
	if opts.SocketPath == "" {
		return nil, ErrSocket
	}
	dir := filepath.Dir(opts.SocketPath)
	if err := prepareDir(dir); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(opts.SocketPath); err == nil {
		return nil, ErrExists
	}
	token := opts.Token
	if token == "" {
		var err error
		token, err = GenerateToken()
		if err != nil {
			return nil, err
		}
	}
	if len(token) < MinTokenBytes || len(token) > MaxTokenBytes {
		return nil, ErrToken
	}
	if opts.TokenPath != "" {
		if err := WriteTokenFile(opts.TokenPath, token); err != nil {
			return nil, err
		}
	}

	var proxyLn net.Listener
	var proxyAddr string
	if opts.ProxyUpstream != "" {
		ln, err := net.Listen("tcp", opts.ProxyListen)
		if err != nil {
			return nil, ErrProxy
		}
		proxyLn = ln
		proxyAddr = ln.Addr().String()
	}

	uln, err := net.Listen("unix", opts.SocketPath)
	if err != nil {
		if proxyLn != nil {
			_ = proxyLn.Close()
		}
		return nil, err
	}
	if err := os.Chmod(opts.SocketPath, 0o600); err != nil {
		_ = uln.Close()
		if proxyLn != nil {
			_ = proxyLn.Close()
		}
		return nil, ErrSocket
	}

	cmd := exec.Command(opts.Command[0], opts.Command[1:]...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		_ = uln.Close()
		if proxyLn != nil {
			_ = proxyLn.Close()
		}
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = uln.Close()
		if proxyLn != nil {
			_ = proxyLn.Close()
		}
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = uln.Close()
		if proxyLn != nil {
			_ = proxyLn.Close()
		}
		return nil, err
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	env := opts.Env
	if env == nil {
		env = os.Environ()
	}
	env = stripEnv(env)
	env = append(env, "SAHLINK_SOCKET="+opts.SocketPath)
	if opts.TokenPath != "" {
		env = append(env, "SAHLINK_TOKEN_FILE="+opts.TokenPath)
	}
	if proxyAddr != "" {
		env = append(env, "SAHLINK_PROXY="+proxyAddr)
	}
	cmd.Env = env
	if err := cmd.Start(); err != nil {
		_ = uln.Close()
		if proxyLn != nil {
			_ = proxyLn.Close()
		}
		return nil, err
	}

	s := &Session{
		cmd:        cmd,
		stdin:      stdin,
		streams:    map[string]*streamBuf{},
		socketPath: opts.SocketPath,
		token:      token,
		tokenPath:  opts.TokenPath,
		proxyAddr:  proxyAddr,
		upstream:   opts.ProxyUpstream,
		ln:         uln,
		proxyLn:    proxyLn,
	}
	for _, name := range []string{"stdin", "stdout", "stderr", "socket-rx", "socket-tx"} {
		s.streams[name] = newStream(opts.MaxCapture)
	}
	go s.copyOut(stdout, "stdout", opts.MirrorStdout)
	go s.copyOut(stderr, "stderr", opts.MirrorStderr)
	go s.serveControl(uln)
	if proxyLn != nil {
		go s.serveProxy(proxyLn)
	}
	return s, nil
}

func prepareDir(dir string) error {
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		return ErrSocket
	}
	if st.Mode().Perm() != 0o700 {
		return ErrSocket
	}
	return nil
}

func stripEnv(env []string) []string {
	prefixes := []string{"SAHLINK_TOKEN=", "SAHLINK_SOCKET=", "SAHLINK_TOKEN_FILE=", "SAHLINK_PROXY="}
	out := make([]string, 0, len(env)+4)
	for _, entry := range env {
		drop := false
		for _, prefix := range prefixes {
			if strings.HasPrefix(entry, prefix) {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, entry)
		}
	}
	return out
}

func (s *Session) ProxyAddr() string { return s.proxyAddr }

func (s *Session) SocketPath() string { return s.socketPath }

func (s *Session) copyOut(r io.Reader, name string, mirror io.Writer) {
	buf := make([]byte, 32<<10)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			_, _ = s.streams[name].Write(chunk)
			if mirror != nil {
				// A broken terminal must not drop the capture.
				_, _ = mirror.Write(chunk)
			}
		}
		if err != nil {
			return
		}
	}
}

// WriteStdin writes to the child and records the bytes on the stdin stream.
func (s *Session) WriteStdin(p []byte) error {
	if len(p) > maxWrite {
		return ErrLarge
	}
	s.stdinMu.Lock()
	defer s.stdinMu.Unlock()
	if s.stdin == nil {
		return ErrClosed
	}
	if _, err := s.stdin.Write(p); err != nil {
		return err
	}
	_, _ = s.streams["stdin"].Write(p)
	return nil
}

// CloseStdin closes the child stdin. A second call is a no-op.
func (s *Session) CloseStdin() error {
	s.stdinMu.Lock()
	defer s.stdinMu.Unlock()
	if s.stdin == nil {
		return nil
	}
	err := s.stdin.Close()
	s.stdin = nil
	return err
}

// Wait waits for the child and returns its exit code.
func (s *Session) Wait() (int, error) {
	s.waitOnce.Do(func() {
		err := s.cmd.Wait()
		s.waitErr = err
		var exit *exec.ExitError
		switch {
		case err == nil:
			s.exitCode = 0
		case errors.As(err, &exit):
			s.exitCode = exit.ExitCode()
		default:
			s.exitCode = -1
		}
	})
	return s.exitCode, s.waitErr
}

// Close stops listeners and signals the child process group.
func (s *Session) Close() error {
	s.closeOnce.Do(func() {
		s.closeErr = s.shutdown()
	})
	return s.closeErr
}

func (s *Session) shutdown() error {
	var first error
	if s.ln != nil {
		if err := s.ln.Close(); err != nil && !errors.Is(err, net.ErrClosed) && first == nil {
			first = err
		}
	}
	if s.proxyLn != nil {
		if err := s.proxyLn.Close(); err != nil && !errors.Is(err, net.ErrClosed) && first == nil {
			first = err
		}
	}
	s.linkMu.Lock()
	if s.clientConn != nil {
		_ = s.clientConn.Close()
	}
	if s.upstreamConn != nil {
		_ = s.upstreamConn.Close()
	}
	s.linkMu.Unlock()
	if err := s.CloseStdin(); err != nil && first == nil {
		first = err
	}
	if s.cmd != nil && s.cmd.Process != nil && s.cmd.Process.Pid > 0 {
		err := syscall.Kill(-s.cmd.Process.Pid, syscall.SIGTERM)
		if err != nil && !errors.Is(err, syscall.ESRCH) && first == nil {
			first = err
		}
	}
	return first
}

func (s *Session) serveControl(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go s.serveConn(conn)
	}
}

func (s *Session) serveConn(conn net.Conn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	rd := bufio.NewReaderSize(conn, 4096)
	line, err := readLine(rd, 4096)
	if err != nil {
		writeErr(conn, ErrAuth.Error())
		return
	}
	var auth request
	if json.Unmarshal(line, &auth) != nil || auth.Op != "auth" || len(auth.Token) < MinTokenBytes || len(auth.Token) > MaxTokenBytes || !TokenEqual(auth.Token, s.token) {
		writeErr(conn, ErrAuth.Error())
		return
	}
	writeOK(conn, map[string]any{"ok": true, "op": "auth"})
	for i := 0; i < maxCommands; i++ {
		_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
		line, err := readLine(rd, maxLine)
		if err != nil {
			return
		}
		var req request
		if json.Unmarshal(line, &req) != nil {
			writeErr(conn, ErrRequest.Error())
			continue
		}
		s.handle(conn, req)
	}
}

func readLine(rd *bufio.Reader, max int) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := rd.ReadSlice('\n')
		buf = append(buf, chunk...)
		if len(buf) > max {
			return nil, ErrLarge
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return buf, err
	}
}

func writeErr(w io.Writer, code string) {
	writeOK(w, map[string]any{"ok": false, "error": code})
}

func writeOK(w io.Writer, v any) {
	enc := json.NewEncoder(w)
	_ = enc.Encode(v)
}

func (s *Session) handle(w io.Writer, req request) {
	switch req.Op {
	case "status":
		writeOK(w, s.status())
	case "read":
		if req.Offset < 0 || req.Max < 0 || req.Max > maxWrite {
			writeErr(w, ErrRequest.Error())
			return
		}
		max := req.Max
		if max == 0 {
			max = 4096
		}
		buf, ok := s.streams[req.Stream]
		if !ok {
			writeErr(w, ErrStream.Error())
			return
		}
		b, err := buf.ReadFrom(req.Offset, max)
		if err != nil {
			writeErr(w, err.Error())
			return
		}
		writeOK(w, map[string]any{
			"ok":     true,
			"op":     "read",
			"stream": req.Stream,
			"offset": req.Offset,
			"next":   req.Offset + len(b),
			"data":   base64.StdEncoding.EncodeToString(b),
		})
	case "write":
		payload, err := decodeData(req.Data)
		if err != nil {
			writeErr(w, err.Error())
			return
		}
		if err := s.writeStream(req.Stream, payload); err != nil {
			code := ErrRequest.Error()
			if errors.Is(err, ErrLarge) || errors.Is(err, ErrClosed) || errors.Is(err, ErrStream) {
				code = err.Error()
			}
			writeErr(w, code)
			return
		}
		writeOK(w, map[string]any{"ok": true, "op": "write", "stream": req.Stream, "n": len(payload)})
	case "close-stdin":
		if err := s.CloseStdin(); err != nil {
			writeErr(w, ErrClosed.Error())
			return
		}
		writeOK(w, map[string]any{"ok": true, "op": "close-stdin"})
	default:
		writeErr(w, ErrRequest.Error())
	}
}

func decodeData(s string) ([]byte, error) {
	if len(s) > 96<<10 {
		return nil, ErrLarge
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		b, err = base64.RawStdEncoding.DecodeString(s)
	}
	if err != nil {
		return nil, ErrRequest
	}
	if len(b) > maxWrite {
		return nil, ErrLarge
	}
	return b, nil
}

func (s *Session) writeStream(name string, p []byte) error {
	switch name {
	case "stdin":
		return s.WriteStdin(p)
	case "socket-rx":
		return s.writeConn(false, p)
	case "socket-tx":
		return s.writeConn(true, p)
	default:
		return ErrStream
	}
}

func (s *Session) writeConn(toClient bool, p []byte) error {
	s.linkMu.Lock()
	var dst net.Conn
	var writeMu *sync.Mutex
	stream := "socket-rx"
	if toClient {
		dst = s.clientConn
		writeMu = &s.clientWrite
		stream = "socket-tx"
	} else {
		dst = s.upstreamConn
		writeMu = &s.upstreamWrite
	}
	s.linkMu.Unlock()
	if dst == nil {
		return ErrClosed
	}
	writeMu.Lock()
	_, err := dst.Write(p)
	writeMu.Unlock()
	if err != nil {
		return err
	}
	_, _ = s.streams[stream].Write(p)
	return nil
}

func (s *Session) status() map[string]any {
	out := map[string]any{
		"ok":    true,
		"op":    "status",
		"pid":   0,
		"proxy": s.proxyAddr,
	}
	if s.cmd != nil && s.cmd.Process != nil {
		out["pid"] = s.cmd.Process.Pid
	}
	streams := map[string]any{}
	for name, buf := range s.streams {
		base, n := buf.Len()
		streams[name] = map[string]int{"base": base, "retained": n}
	}
	out["streams"] = streams
	return out
}

func (s *Session) serveProxy(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go s.handleProxy(conn)
	}
}

func (s *Session) handleProxy(conn net.Conn) {
	host, _, err := net.SplitHostPort(s.upstream)
	if err != nil || rejectUpstream(host) != nil {
		_ = conn.Close()
		return
	}
	upstream, err := net.DialTimeout("tcp", s.upstream, 5*time.Second)
	if err != nil {
		_ = conn.Close()
		return
	}
	s.linkMu.Lock()
	if s.clientConn != nil {
		_ = s.clientConn.Close()
		_ = s.upstreamConn.Close()
	}
	s.clientConn = conn
	s.upstreamConn = upstream
	s.linkMu.Unlock()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		s.pump(conn, upstream, "socket-rx")
	}()
	go func() {
		defer wg.Done()
		s.pump(upstream, conn, "socket-tx")
	}()
	wg.Wait()
	_ = conn.Close()
	_ = upstream.Close()
	s.linkMu.Lock()
	if s.clientConn == conn {
		s.clientConn = nil
		s.upstreamConn = nil
	}
	s.linkMu.Unlock()
}

func (s *Session) pump(src, dst net.Conn, stream string) {
	buf := make([]byte, 32<<10)
	for {
		_ = src.SetReadDeadline(time.Now().Add(60 * time.Second))
		n, err := src.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			var writeMu *sync.Mutex
			if stream == "socket-tx" {
				writeMu = &s.clientWrite
			} else {
				writeMu = &s.upstreamWrite
			}
			writeMu.Lock()
			_, werr := dst.Write(chunk)
			writeMu.Unlock()
			if werr != nil {
				return
			}
			_, _ = s.streams[stream].Write(chunk)
		}
		if err != nil {
			return
		}
	}
}

// Client is a control-socket caller. The token is sent once and is not
// included in later errors.
type Client struct {
	conn net.Conn
	rd   *bufio.Reader
	mu   sync.Mutex
}

// Dial authenticates to a session socket.
func Dial(socketPath, token string) (*Client, error) {
	if len(token) < MinTokenBytes || len(token) > MaxTokenBytes {
		return nil, ErrAuth
	}
	conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
	if err != nil {
		return nil, err
	}
	c := &Client{conn: conn, rd: bufio.NewReaderSize(conn, 4096)}
	if err := c.send(request{Op: "auth", Token: token}); err != nil {
		_ = conn.Close()
		return nil, ErrAuth
	}
	resp, err := c.recv()
	if err != nil || resp["ok"] != true {
		_ = conn.Close()
		return nil, ErrAuth
	}
	return c, nil
}

func (c *Client) call(req request) (map[string]any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.send(req); err != nil {
		return nil, err
	}
	return c.recv()
}

// Status returns the session status object.
func (c *Client) Status() (map[string]any, error) {
	return c.call(request{Op: "status"})
}

// Read returns a capture window. Data in the result is standard base64.
func (c *Client) Read(stream string, offset, max int) (map[string]any, error) {
	return c.call(request{Op: "read", Stream: stream, Offset: offset, Max: max})
}

// Write sends bytes to stdin or to the active relay direction.
// socket-rx writes toward the upstream. socket-tx writes toward the dialer.
func (c *Client) Write(stream string, data []byte) (map[string]any, error) {
	if len(data) > maxWrite {
		return nil, ErrLarge
	}
	return c.call(writeRequest(stream, data))
}

// CloseChildStdin tells the session to close the child stdin.
func (c *Client) CloseChildStdin() (map[string]any, error) {
	return c.call(request{Op: "close-stdin"})
}

func (c *Client) send(req request) error {
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	if len(b) > maxLine {
		return ErrLarge
	}
	_, err = c.conn.Write(append(b, '\n'))
	return err
}

func (c *Client) recv() (map[string]any, error) {
	_ = c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := readLine(c.rd, maxLine)
	if err != nil {
		return nil, err
	}
	var resp map[string]any
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, ErrRequest
	}
	return resp, nil
}

// Close closes the control connection.
func (c *Client) Close() error { return c.conn.Close() }

func writeRequest(stream string, data []byte) request {
	return request{Op: "write", Stream: stream, Data: base64.StdEncoding.EncodeToString(data)}
}
