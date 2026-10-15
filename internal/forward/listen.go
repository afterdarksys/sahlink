package forward

import (
	"context"
	"errors"
	"net"
	"strconv"
	"sync"
)

// CheckBindAddr accepts only 127.0.0.1. A host of 0.0.0.0, ::, localhost,
// or any other loopback address is bind_refused. There is no flag that
// widens this.
func CheckBindAddr(host string) error {
	if host == "127.0.0.1" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip != nil && ip.Equal(net.ParseIP("127.0.0.1")) {
		return nil
	}
	return &StatusError{Code: "bind_refused"}
}

// ListenLocal binds tcp4 on 127.0.0.1. port 0 asks the kernel for a free port.
func ListenLocal(port int) (net.Listener, error) {
	if port < 0 || port > 65535 {
		return nil, &StatusError{Code: "bind_refused"}
	}
	addr := "127.0.0.1:" + strconv.Itoa(port)
	if err := CheckBindAddr("127.0.0.1"); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp4", addr)
	if err != nil {
		return nil, &StatusError{Code: "bind_refused"}
	}
	if err := CheckBindAddr(hostOf(ln.Addr().String())); err != nil {
		_ = ln.Close()
		return nil, err
	}
	return ln, nil
}

func hostOf(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}

// Serve accepts on ln until ctx is canceled. open is called once per
// accepted connection and owns that connection's protocol. A non-nil error
// from open is remembered. When the listener stops, Serve returns that error
// so the process can exit non-zero. A canceled context alone is a clean stop.
func Serve(ctx context.Context, ln net.Listener, open func(context.Context, net.Conn) error) error {
	if ln == nil || open == nil {
		return &StatusError{Code: "internal"}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	var wg sync.WaitGroup
	var mu sync.Mutex
	var failed error
	note := func(err error) {
		if err == nil || errors.Is(err, context.Canceled) {
			return
		}
		mu.Lock()
		if failed == nil {
			failed = err
		}
		mu.Unlock()
	}
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				wg.Wait()
				return failed
			}
			return err
		}
		if !remoteIsLoopback(c.RemoteAddr()) {
			_ = c.Close()
			note(&StatusError{Code: "bind_refused"})
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer c.Close()
			note(open(ctx, c))
		}()
	}
}

func remoteIsLoopback(addr net.Addr) bool {
	if addr == nil {
		return false
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		host = addr.String()
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.Equal(net.ParseIP("127.0.0.1"))
}
