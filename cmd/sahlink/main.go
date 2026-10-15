package main

import (
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"strings"

	"github.com/afterdarksys/sahlink/internal/session"
)

const version = "0.2.0"

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return 2
	}
	switch args[0] {
	case "version", "--version":
		fmt.Println("sahlink " + version)
		return 0
	case "help", "-h", "--help":
		usage()
		return 0
	case "attach", "ptrace":
		fmt.Fprintln(os.Stderr, "attach_unsupported")
		return 2
	case "run":
		return cmdRun(args[1:])
	case "io":
		return cmdIO(args[1:])
	case "rdp":
		return cmdRDP(args[1:])
	default:
		if hasReverse(args) {
			fmt.Fprintln(os.Stderr, "reverse_unsupported")
			fmt.Fprintln(os.Stderr, "see docs/reverse-forward.md")
			return 2
		}
		if isForward(args) {
			return cmdForward(args)
		}
		usage()
		return 2
	}
}

func hasReverse(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "run", "io", "version", "--version", "help", "-h", "--help", "attach", "ptrace", "rdp":
		return false
	}
	for _, a := range args {
		if a == "-R" || a == "--remote" || a == "--reverse" {
			return true
		}
	}
	return false
}

func isForward(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "-L", "--local", "--via", "--launch", "--port":
		return true
	}
	return strings.Contains(args[0], ":") || strings.HasPrefix(args[0], "[")
}

func usage() {
	fmt.Fprint(os.Stderr, `sahlink `+version+` — TCP forward and a capture tool for a program this tool starts

  sahlink HOST:PORT
  sahlink -L LOCALPORT HOST:PORT
  sahlink --via AGENT HOST:PORT
  sahlink rdp [--port PORT] [--launch] HOST
  sahlink version
  sahlink run [--proxy-listen 127.0.0.1:0 --proxy-upstream HOST:PORT] [--max-bytes N] -- COMMAND...
  sahlink io --socket PATH --token-file PATH status
  sahlink io --socket PATH --token-file PATH read STREAM
  sahlink io --socket PATH --token-file PATH write STREAM

HOST:PORT listens on 127.0.0.1 and asks that host's agent to dial 127.0.0.1:PORT.
--via AGENT dials HOST:PORT from AGENT instead. -R is not available.
Login is sahconnect login. The listener prints 127.0.0.1:PORT.

sahlink run starts COMMAND. It records stdin, stdout, and stderr. It does not
attach to an existing process. The socket relay listens on loopback and dials
only the upstream named with --proxy-upstream. The control token is written
to a mode 0600 file and is not printed.
`)
}

func cmdRun(args []string) int {
	_, opts, rest, code := parseRun(args)
	if code != 0 {
		return code
	}
	if len(rest) == 0 {
		fmt.Fprintln(os.Stderr, "command_required")
		return 2
	}
	dir := opts.dir
	var err error
	if dir == "" {
		dir, err = os.MkdirTemp("", "sahlink-")
		if err != nil {
			fmt.Fprintln(os.Stderr, "socket_dir_rejected")
			return 1
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			fmt.Fprintln(os.Stderr, "socket_dir_rejected")
			return 1
		}
	}
	socketPath := filepath.Join(dir, "control.sock")
	tokenPath := filepath.Join(dir, "token")
	s, err := session.Start(session.Options{
		Command:       rest,
		SocketPath:    socketPath,
		TokenPath:     tokenPath,
		MaxCapture:    opts.max,
		MirrorStdout:  os.Stdout,
		MirrorStderr:  os.Stderr,
		ProxyListen:   opts.listen,
		ProxyUpstream: opts.upstream,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 1
	}
	fmt.Fprintf(os.Stderr, "socket=%s\ntoken_file=%s\n", socketPath, tokenPath)
	go copyStdin(s)
	code, _ = s.Wait()
	_ = s.Close()
	if code < 0 {
		return 1
	}
	return code
}

type runOpts struct {
	listen   string
	upstream string
	dir      string
	max      int
}

func parseRun(args []string) (ok bool, opts runOpts, rest []string, code int) {
	// Manual parse so a flag named pid, attach, or ptrace is a hard refusal
	// rather than a generic usage error.
	var restArgs []string
	opts.max = session.DefaultMaxCapture
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			restArgs = append(restArgs, args[i+1:]...)
			break
		}
		switch a {
		case "--pid", "--attach", "--ptrace":
			fmt.Fprintln(os.Stderr, "attach_unsupported")
			return false, opts, nil, 2
		case "--proxy-listen", "--proxy-upstream", "--dir", "--max-bytes":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "usage")
				return false, opts, nil, 2
			}
			i++
			switch a {
			case "--proxy-listen":
				opts.listen = args[i]
			case "--proxy-upstream":
				opts.upstream = args[i]
			case "--dir":
				opts.dir = args[i]
			case "--max-bytes":
				n, err := atoi(args[i])
				if err != nil || n <= 0 {
					fmt.Fprintln(os.Stderr, "usage")
					return false, opts, nil, 2
				}
				opts.max = n
			}
		default:
			if len(a) > 0 && a[0] == '-' {
				fmt.Fprintln(os.Stderr, "usage")
				return false, opts, nil, 2
			}
			restArgs = append(restArgs, a)
		}
	}
	return true, opts, restArgs, 0
}

func atoi(s string) (int, error) {
	n := 0
	if s == "" {
		return 0, fmt.Errorf("bad")
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("bad")
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}

func copyStdin(s *session.Session) {
	buf := make([]byte, 4096)
	for {
		n, err := os.Stdin.Read(buf)
		if n > 0 {
			if werr := s.WriteStdin(append([]byte(nil), buf[:n]...)); werr != nil {
				return
			}
		}
		if err != nil {
			_ = s.CloseStdin()
			return
		}
	}
}

func cmdIO(args []string) int {
	var socketPath, tokenPath, op, stream string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--socket", "--token-file":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "usage")
				return 2
			}
			if args[i] == "--socket" {
				socketPath = args[i+1]
			} else {
				tokenPath = args[i+1]
			}
			i++
		case "status", "read", "write":
			op = args[i]
			if op != "status" {
				if i+1 >= len(args) {
					fmt.Fprintln(os.Stderr, "usage")
					return 2
				}
				i++
				stream = args[i]
			}
		default:
			fmt.Fprintln(os.Stderr, "usage")
			return 2
		}
	}
	if socketPath == "" || tokenPath == "" || op == "" {
		fmt.Fprintln(os.Stderr, "usage")
		return 2
	}
	token, err := session.ReadTokenFile(tokenPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "token_file_rejected")
		return 1
	}
	c, err := session.Dial(socketPath, token)
	if err != nil {
		fmt.Fprintln(os.Stderr, "auth_denied")
		return 1
	}
	defer c.Close()
	var resp map[string]any
	switch op {
	case "status":
		resp, err = c.Status()
	case "read":
		resp, err = c.Read(stream, 0, 65536)
	case "write":
		body, rerr := io.ReadAll(io.LimitReader(os.Stdin, 64<<10+1))
		if rerr != nil || len(body) > 64<<10 {
			fmt.Fprintln(os.Stderr, "too_large")
			return 1
		}
		resp, err = c.Write(stream, body)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "request_failed")
		return 1
	}
	if resp["ok"] != true {
		if code, _ := resp["error"].(string); code != "" {
			fmt.Fprintln(os.Stderr, code)
		} else {
			fmt.Fprintln(os.Stderr, "request_failed")
		}
		return 1
	}
	if op == "read" {
		raw, _ := resp["data"].(string)
		b, decErr := base64.StdEncoding.DecodeString(raw)
		if decErr != nil {
			fmt.Fprintln(os.Stderr, "bad_request")
			return 1
		}
		_, _ = os.Stdout.Write(b)
		return 0
	}
	fmt.Printf("ok %s\n", op)
	return 0
}
