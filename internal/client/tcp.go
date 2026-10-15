package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"

	"github.com/afterdarksys/sahlink/internal/target"
)

// PurposeTCP is the only purpose this client requests for a forward.
const PurposeTCP = "tcp:open"

// TCPTicket requests one single-use tcp:open ticket bound to host and target.
// The ticket value is returned to the caller and must only be placed in the
// WebSocket query. It is never printed.
func (c *Client) TCPTicket(ctx context.Context, hostID, targetHost string, port int) (*Ticket, error) {
	if c == nil || c.Base == nil {
		return nil, code("invalid_url")
	}
	if !ValidHostName(hostID) {
		return nil, code("invalid_host_id")
	}
	if port < 1 || port > 65535 {
		return nil, code("bad_target")
	}
	if err := target.Check(targetHost); err != nil {
		return nil, err
	}
	req, err := json.Marshal(struct {
		HostID  string `json:"host_id"`
		Purpose string `json:"purpose"`
		Target  struct {
			Host string `json:"host"`
			Port int    `json:"port"`
		} `json:"target"`
	}{
		HostID: hostID, Purpose: PurposeTCP,
		Target: struct {
			Host string `json:"host"`
			Port int    `json:"port"`
		}{Host: targetHost, Port: port},
	})
	if err != nil {
		return nil, code("bad_request")
	}
	status, body, err := c.Do(ctx, http.MethodPost, "/api/v1/ws-ticket", "", req)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK && status != http.StatusCreated {
		var eb errBody
		_ = json.Unmarshal(body, &eb)
		if !errorCodeRE.MatchString(eb.Error) {
			eb.Error = ""
		}
		return nil, &TicketError{Status: status, Code: eb.Error}
	}
	var out struct {
		Ticket    string `json:"ticket"`
		HostID    string `json:"host_id"`
		Purpose   string `json:"purpose"`
		ExpiresIn *int   `json:"expires_in"`
	}
	if json.Unmarshal(body, &out) != nil || !validSecret(out.Ticket) {
		return nil, code("bad_response")
	}
	if out.HostID != hostID || out.Purpose != PurposeTCP {
		return nil, code("bad_response")
	}
	if out.ExpiresIn != nil && *out.ExpiresIn != 60 {
		return nil, code("bad_response")
	}
	return &Ticket{Value: out.Ticket}, nil
}

// TCPURL builds the TCP WebSocket URL. The ticket is the only query secret.
func TCPURL(base *url.URL, hostID, ticket string) (*url.URL, error) {
	if base == nil {
		return nil, code("invalid_url")
	}
	if !ValidHostName(hostID) {
		return nil, code("invalid_host_id")
	}
	if !validSecret(ticket) {
		return nil, code("invalid_ticket")
	}
	u := *base
	switch base.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		return nil, code("invalid_url")
	}
	u.Path = "/api/v1/systems/tcp/" + hostID
	q := url.Values{}
	q.Set("ticket", ticket)
	q.Set("purpose", PurposeTCP)
	u.RawQuery = q.Encode()
	u.User = nil
	u.Fragment = ""
	if err := ValidateWS(&u); err != nil {
		return nil, err
	}
	for key := range q {
		switch key {
		case "ticket", "purpose":
		default:
			return nil, code("invalid_query")
		}
	}
	return &u, nil
}

// PortString is the local port printed to the operator. It is not a secret.
func PortString(p int) string { return strconv.Itoa(p) }
