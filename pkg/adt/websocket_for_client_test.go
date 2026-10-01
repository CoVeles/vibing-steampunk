package adt

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// upgradeRecorder refuses every request and keeps the headers of the last
// WebSocket upgrade it saw.
func upgradeRecorder(t *testing.T) (*httptest.Server, func() http.Header) {
	t.Helper()
	var mu sync.Mutex
	var last http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "" {
			mu.Lock()
			last = r.Header.Clone()
			mu.Unlock()
		}
		w.WriteHeader(http.StatusForbidden)
	}))
	t.Cleanup(srv.Close)
	return srv, func() http.Header {
		mu.Lock()
		defer mu.Unlock()
		return last
	}
}

func TestWebSocketFromClient_CarriesTheSessionNotThePassword(t *testing.T) {
	srv, last := upgradeRecorder(t)
	c := NewClient(srv.URL, "TESTUSER", "s3cret", WithClient("100"),
		WithCookies(map[string]string{"MYSAPSSO2": "startup"}))
	// A re-authentication replaces the session wholesale.
	c.SetCookies(map[string]string{"MYSAPSSO2": "renewed"})

	for name, connect := range map[string]func(context.Context) error{
		"debug": c.NewDebugWebSocketClient().Connect,
		"amdp":  c.NewAMDPWebSocketClient().Connect,
	} {
		if err := connect(context.Background()); err == nil {
			t.Fatalf("%s: the server refuses every upgrade; Connect succeeded", name)
		}
		h := last()
		if h == nil {
			t.Fatalf("%s: no WebSocket upgrade reached the server", name)
		}
		if got := h.Get("Cookie"); got != "MYSAPSSO2=renewed" {
			t.Errorf("%s: upgrade carried cookie %q, want the client's current session", name, got)
		}
		if got := h.Get("Authorization"); got != "" {
			t.Errorf("%s: upgrade carried Authorization %q alongside the session", name, got)
		}
	}

	if _, password, _ := c.wsCredentials(); password != "" {
		t.Error("a WebSocket built for a session-authenticated client was handed the password")
	}
}

func TestWebSocketFromClient_PasswordWithoutSession(t *testing.T) {
	srv, last := upgradeRecorder(t)
	c := NewClient(srv.URL, "TESTUSER", "s3cret", WithClient("100"))

	if err := c.NewDebugWebSocketClient().Connect(context.Background()); err == nil {
		t.Fatal("the server refuses every upgrade; Connect succeeded")
	}
	h := last()
	if h == nil {
		t.Fatal("no WebSocket upgrade reached the server")
	}
	want, _ := http.NewRequest(http.MethodGet, "/", nil)
	want.SetBasicAuth("TESTUSER", "s3cret")
	if got := h.Get("Authorization"); got != want.Header.Get("Authorization") {
		t.Errorf("upgrade carried Authorization %q, want the client's basic auth", got)
	}
	if got := h.Get("Cookie"); got != "" {
		t.Errorf("upgrade carried cookie %q from a password client", got)
	}
}

// The APC probe in the compatibility check builds its WebSocket the same way,
// so it sees the session the client holds now.
func TestAPCProbeUsesTheCurrentSession(t *testing.T) {
	srv, last := upgradeRecorder(t)
	c := NewClient(srv.URL, "", "", WithClient("100"),
		WithCookies(map[string]string{"MYSAPSSO2": "startup"}))
	c.SetCookies(map[string]string{"MYSAPSSO2": "renewed"})

	c.probeAPCTunnel(context.Background(), CompatCheck{ID: apcCheckID})
	h := last()
	if h == nil {
		t.Fatal("the probe never attempted the WebSocket upgrade")
	}
	if got := h.Get("Cookie"); got != "MYSAPSSO2=renewed" {
		t.Errorf("probe carried cookie %q, want the renewed session", got)
	}
}
