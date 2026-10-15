package client

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func randSecret(t *testing.T) string {
	t.Helper()
	var b [18]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return "stk_" + base64.RawURLEncoding.EncodeToString(b[:])
}

func TestTCPTicketShape(t *testing.T) {
	secret := randSecret(t)
	var body []byte
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			t.Errorf("query %s", r.URL.RawQuery)
		}
		for _, k := range []string{"api_key", "access_token", "token", "device_code"} {
			if r.URL.Query().Has(k) {
				t.Errorf("query key %s", k)
			}
		}
		auth = r.Header.Get("Authorization")
		var err error
		body, err = io.ReadAll(io.LimitReader(r.Body, 1<<16))
		if err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ticket":"` + secret + `","host_id":"web1","purpose":"tcp:open","expires_in":60}`))
	}))
	defer srv.Close()
	base, err := ValidateBase(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	c := &Client{Base: base, Auth: Auth{APIKey: randSecret(t)}, HTTP: srv.Client()}
	tk, err := c.TCPTicket(context.Background(), "web1", "local", 5432)
	if err != nil {
		t.Fatal(err)
	}
	if tk.Value != secret {
		t.Fatal("ticket mismatch")
	}
	if auth != "" {
		t.Fatal("bearer sent")
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(body, &raw) != nil {
		t.Fatalf("body %s", body)
	}
	var portRaw map[string]json.RawMessage
	if json.Unmarshal(raw["target"], &portRaw) != nil {
		t.Fatal("target")
	}
	if string(portRaw["port"]) != "5432" {
		t.Fatalf("port %s", portRaw["port"])
	}
	if strings.Contains(string(portRaw["port"]), `"`) {
		t.Fatal("port was a string")
	}
	var whole struct {
		Purpose string `json:"purpose"`
		Target  struct {
			Host string `json:"host"`
		} `json:"target"`
	}
	if json.Unmarshal(body, &whole) != nil || whole.Purpose != PurposeTCP || whole.Target.Host != "local" {
		t.Fatalf("%s", body)
	}
	u, err := TCPURL(base, "web1", tk.Value)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(u.Path, "/api/v1/systems/tcp/web1") {
		t.Fatal(u.Path)
	}
	q := u.Query()
	if len(q) != 2 || q.Get("purpose") != PurposeTCP || q.Get("ticket") != secret {
		t.Fatalf("query %v", q)
	}
	for _, k := range []string{"api_key", "access_token", "token", "device_code"} {
		if q.Has(k) {
			t.Fatal(k)
		}
	}
}

func TestTCPTicketRefusals(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		http.Error(w, "no", http.StatusForbidden)
	}))
	defer srv.Close()
	base, err := ValidateBase(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	bearer := randSecret(t)
	c := &Client{Base: base, Auth: Auth{Bearer: bearer, BearerOrigin: "https://other.example"}, HTTP: srv.Client()}
	_, err = c.TCPTicket(context.Background(), "web1", "local", 1)
	var ce *Error
	if !errors.As(err, &ce) || ce.Code != "token_origin_mismatch" || called {
		t.Fatal(err, called)
	}
	c.Auth = Auth{APIKey: randSecret(t)}
	for _, host := range []string{"127.0.0.1", "example.com", "0.0.0.0", "169.254.169.254"} {
		called = false
		_, err = c.TCPTicket(context.Background(), "web1", host, 80)
		if err == nil || called {
			t.Fatalf("%s err=%v called=%v", host, err, called)
		}
	}
}

func TestTCPTicketServerCodes(t *testing.T) {
	secret := randSecret(t)
	status := http.StatusForbidden
	payload := `{"error":"target_denied"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()
	base, err := ValidateBase(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	c := &Client{Base: base, Auth: Auth{APIKey: randSecret(t)}, HTTP: srv.Client()}
	_, err = c.TCPTicket(context.Background(), "web1", "10.0.0.7", 3389)
	var te *TicketError
	if !errors.As(err, &te) || te.Code != "target_denied" {
		t.Fatal(err)
	}
	status = http.StatusOK
	payload = `{"ticket":"` + secret + `","host_id":"other","purpose":"tcp:open","expires_in":60}`
	_, err = c.TCPTicket(context.Background(), "web1", "local", 9)
	var ce *Error
	if !errors.As(err, &ce) || ce.Code != "bad_response" {
		t.Fatal(err)
	}
	payload = `{"ticket":"` + secret + `","host_id":"web1","purpose":"terminal:open","expires_in":60}`
	_, err = c.TCPTicket(context.Background(), "web1", "local", 9)
	if !errors.As(err, &ce) || ce.Code != "bad_response" {
		t.Fatal(err)
	}
	payload = `{"ticket":"` + secret + `","host_id":"web1","purpose":"tcp:open"}`
	tk, err := c.TCPTicket(context.Background(), "web1", "10.0.0.7", 3389)
	if err != nil || tk.Value != secret {
		t.Fatal(err)
	}
}
