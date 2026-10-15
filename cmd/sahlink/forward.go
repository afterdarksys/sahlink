package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/afterdarksys/sahlink/internal/client"
	"github.com/afterdarksys/sahlink/internal/config"
	"github.com/afterdarksys/sahlink/internal/forward"
	"github.com/afterdarksys/sahlink/internal/rdp"
	"github.com/afterdarksys/sahlink/internal/target"
)

// forwardSpec is one local listener and one remote target.
type forwardSpec struct {
	localPort int
	hasLocal  bool
	via       string
	launch    bool
	rdp       bool
	agent     string
	destHost  string
	destPort  int
}

func cmdForward(args []string) int { return runForward(args, false) }

func cmdRDP(args []string) int { return runForward(args, true) }

func runForward(args []string, rdpMode bool) int {
	spec, code := parseForward(args, rdpMode)
	if code != 0 {
		return code
	}
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 2
	}
	if cfg.SystemAPIURL == "" {
		fmt.Fprintln(os.Stderr, "systemapi_url is not set")
		return 2
	}
	if !hasAgent(cfg.Methods) {
		fmt.Fprintln(os.Stderr, "method ssh is not available until phase 3 (it needs a signed connect policy); use method agent")
		return 2
	}
	base, err := client.ValidateBase(cfg.SystemAPIURL)
	if err != nil {
		printErr(err)
		return 2
	}
	auth, note, err := client.LoadAuth(cfg, base)
	if note != "" {
		fmt.Fprintln(os.Stderr, note)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		return 1
	}
	api := &client.Client{Base: base, Auth: auth, HTTP: client.NewHTTPClient(nil, 20*time.Second)}
	wsHTTP := client.NewHTTPClient(nil, 0)
	ctx, stop := signalContext()
	defer stop()
	host, err := api.Resolve(ctx, spec.agent)
	if err != nil {
		printErr(err)
		return 1
	}
	port := 0
	if spec.hasLocal {
		port = spec.localPort
	}
	ln, err := forward.ListenLocal(port)
	if err != nil {
		printErr(err)
		return 1
	}
	defer ln.Close()
	fmt.Fprintln(os.Stdout, ln.Addr().String())
	if spec.rdp {
		_, p, _ := net.SplitHostPort(ln.Addr().String())
		n, _ := strconv.Atoi(p)
		for _, line := range rdp.Lines(n) {
			fmt.Fprintln(os.Stdout, line)
		}
		if spec.launch {
			if lerr := rdp.Launch(n, nil); lerr != nil {
				fmt.Fprintln(os.Stderr, "rdp_client_missing")
			}
		}
	}
	err = forward.Serve(ctx, ln, func(ctx context.Context, local net.Conn) error {
		return openOne(ctx, api, wsHTTP, host.HostID, spec.destHost, spec.destPort, local)
	})
	if err != nil {
		return 1
	}
	return 0
}

func hasAgent(methods []string) bool {
	for _, m := range methods {
		if m == config.MethodAgent {
			return true
		}
	}
	return false
}

func openOne(ctx context.Context, api *client.Client, ws *http.Client, hostID, dest string, port int, local net.Conn) error {
	tk, err := api.TCPTicket(ctx, hostID, dest, port)
	if err != nil {
		if ctx.Err() == nil {
			printErr(err)
		}
		return err
	}
	u, err := client.TCPURL(api.Base, hostID, tk.Value)
	if err != nil {
		printErr(err)
		return err
	}
	err = forward.Dial(ctx, local, u.String(), ws, forward.Options{
		OnClose: func(info forward.CloseInfo) {
			reason := info.Reason
			if reason == "" {
				reason = "closed"
			}
			fmt.Fprintf(os.Stderr, "tcp_close reason=%s bytes_in=%d bytes_out=%d\n", reason, info.BytesIn, info.BytesOut)
		},
	})
	if err != nil && ctx.Err() == nil {
		printErr(err)
	}
	return err
}

func printErr(err error) {
	var se *forward.StatusError
	var ce *client.Error
	var te *target.Error
	var tk *client.TicketError
	var re *client.ResolveError
	switch {
	case errors.As(err, &se):
		fmt.Fprintln(os.Stderr, se.Code)
	case errors.As(err, &te):
		fmt.Fprintln(os.Stderr, te.Error())
	case errors.As(err, &ce):
		fmt.Fprintln(os.Stderr, ce.Code)
	case errors.As(err, &tk):
		if tk.Code != "" {
			fmt.Fprintln(os.Stderr, tk.Code)
		} else if tk.Status == http.StatusTooManyRequests {
			fmt.Fprintln(os.Stderr, "rate_limited")
		} else {
			fmt.Fprintln(os.Stderr, "ticket_invalid")
		}
	case errors.As(err, &re):
		if re.Code != "" {
			fmt.Fprintln(os.Stderr, re.Code)
		} else {
			fmt.Fprintln(os.Stderr, re.Error())
		}
	default:
		msg := err.Error()
		if strings.Contains(msg, "ticket=") || strings.Contains(msg, "://") {
			fmt.Fprintln(os.Stderr, "tcp connection failed")
			return
		}
		fmt.Fprintln(os.Stderr, msg)
	}
}

func parseForward(args []string, rdpMode bool) (forwardSpec, int) {
	var spec forwardSpec
	spec.rdp = rdpMode
	spec.destPort = 3389
	var positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "-R", "--remote", "--reverse":
			fmt.Fprintln(os.Stderr, "reverse_unsupported")
			fmt.Fprintln(os.Stderr, "see docs/reverse-forward.md")
			return spec, 2
		case "-L", "--local":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "bind_refused")
				return spec, 2
			}
			i++
			p, err := atoiPort(args[i])
			if err != nil {
				fmt.Fprintln(os.Stderr, "bind_refused")
				return spec, 2
			}
			spec.localPort = p
			spec.hasLocal = true
		case "--via":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "usage")
				return spec, 2
			}
			i++
			spec.via = args[i]
		case "--launch":
			spec.launch = true
		case "--port":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "bad_target")
				return spec, 2
			}
			i++
			p, err := atoiPort(args[i])
			if err != nil || p < 1 {
				fmt.Fprintln(os.Stderr, "bad_target")
				return spec, 2
			}
			spec.destPort = p
		default:
			if strings.HasPrefix(a, "-") {
				fmt.Fprintln(os.Stderr, "usage")
				return spec, 2
			}
			positional = append(positional, a)
		}
	}
	if len(positional) != 1 {
		fmt.Fprintln(os.Stderr, "usage")
		return spec, 2
	}
	raw := positional[0]
	if rdpMode && !strings.Contains(raw, ":") {
		if !client.ValidHostName(raw) && spec.via == "" {
			fmt.Fprintln(os.Stderr, "invalid_name")
			return spec, 2
		}
		if spec.via == "" {
			spec.agent = raw
			spec.destHost = "local"
		} else {
			if !client.ValidHostName(spec.via) {
				fmt.Fprintln(os.Stderr, "invalid_name")
				return spec, 2
			}
			if err := target.Check(raw); err != nil {
				printErr(err)
				return spec, 2
			}
			spec.agent = spec.via
			spec.destHost = raw
		}
		return spec, 0
	}
	host, port, err := splitHostPort(raw)
	if err != nil {
		fmt.Fprintln(os.Stderr, "bad_target")
		return spec, 2
	}
	spec.destPort = port
	if spec.via == "" {
		if !client.ValidHostName(host) {
			fmt.Fprintln(os.Stderr, "invalid_name")
			return spec, 2
		}
		spec.agent = host
		spec.destHost = "local"
		return spec, 0
	}
	if !client.ValidHostName(spec.via) {
		fmt.Fprintln(os.Stderr, "invalid_name")
		return spec, 2
	}
	if err := target.Check(host); err != nil {
		printErr(err)
		return spec, 2
	}
	spec.agent = spec.via
	spec.destHost = host
	return spec, 0
}

func splitHostPort(spec string) (string, int, error) {
	if spec == "" {
		return "", 0, errors.New("bad_target")
	}
	if spec[0] == '[' {
		end := strings.IndexByte(spec, ']')
		if end <= 1 || end+2 > len(spec) || spec[end+1] != ':' {
			return "", 0, errors.New("bad_target")
		}
		host := spec[1:end]
		if host == "" || strings.Contains(host, "%") {
			return "", 0, errors.New("bad_target")
		}
		p, err := atoiPort(spec[end+2:])
		if err != nil || p < 1 {
			return "", 0, errors.New("bad_target")
		}
		return host, p, nil
	}
	if strings.Count(spec, ":") != 1 {
		return "", 0, errors.New("bad_target")
	}
	i := strings.LastIndexByte(spec, ':')
	host := spec[:i]
	if host == "" {
		return "", 0, errors.New("bad_target")
	}
	p, err := atoiPort(spec[i+1:])
	if err != nil || p < 1 {
		return "", 0, errors.New("bad_target")
	}
	return host, p, nil
}

func atoiPort(s string) (int, error) {
	if s == "" || len(s) > 5 {
		return 0, errors.New("bad")
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, errors.New("bad")
		}
		n = n*10 + int(r-'0')
	}
	if n > 65535 {
		return 0, errors.New("bad")
	}
	return n, nil
}

func signalContext() (context.Context, func()) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	done := make(chan struct{})
	go func() {
		select {
		case <-ch:
			cancel()
		case <-done:
		}
	}()
	return ctx, func() {
		signal.Stop(ch)
		close(done)
		cancel()
	}
}
