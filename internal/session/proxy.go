package session

import (
	"errors"
	"net"
	"strconv"
	"strings"
)

var ErrProxy = errors.New("proxy_rejected")

// ValidateProxy checks an opt-in TCP relay. Both addresses empty means the
// relay is off. The listen address must be a loopback IP. The upstream is
// one host the operator named. Link-local, unspecified, multicast, and
// cloud-metadata addresses are refused.
func ValidateProxy(listen, upstream string) error {
	if listen == "" && upstream == "" {
		return nil
	}
	if listen == "" || upstream == "" {
		return errors.New("proxy_incomplete")
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return ErrProxy
	}
	if _, err := strconv.Atoi(port); err != nil {
		return ErrProxy
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return ErrProxy
	}
	uhost, uport, err := net.SplitHostPort(upstream)
	if err != nil || uport == "" || uport == "0" {
		return ErrProxy
	}
	if _, err := strconv.Atoi(uport); err != nil {
		return ErrProxy
	}
	return rejectUpstream(uhost)
}

func rejectUpstream(host string) error {
	name := strings.TrimSuffix(strings.ToLower(host), ".")
	switch name {
	case "metadata.google.internal", "metadata.goog":
		return ErrProxy
	}
	if ip := net.ParseIP(host); ip != nil {
		if badIP(ip) {
			return ErrProxy
		}
		return nil
	}
	ips, err := net.LookupIP(host)
	if err != nil || len(ips) == 0 {
		return ErrProxy
	}
	for _, ip := range ips {
		if badIP(ip) {
			return ErrProxy
		}
	}
	return nil
}

func badIP(ip net.IP) bool {
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	if ip.IsLoopback() {
		return false
	}
	if ip.Equal(net.ParseIP("fd00:ec2::254")) {
		return true
	}
	return ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() || ip.IsInterfaceLocalMulticast()
}
