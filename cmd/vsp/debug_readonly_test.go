package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oisee/vibing-steampunk/pkg/adt"
)

// debugTestSession is a vsp debug session whose SAP is an httptest server
// that only counts requests. The WebSocket is never connected.
func debugTestSession(t *testing.T, readOnly bool) (*debugSession, func() int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, "no", http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &debugSession{
		client:   adt.NewClient(srv.URL, "TESTUSER", "secret"),
		wsClient: adt.NewDebugWebSocketClient(srv.URL, "001", "TESTUSER", "secret", false),
		user:     "TESTUSER",
		readOnly: readOnly,
		ctx:      ctx,
		cancel:   cancel,
	}, hits.Load
}

// The REPL's "run" submits a report and "call" runs a function module: both
// execute code on the system, so a read-only system refuses them before the
// debugger listener starts or anything is sent.
func TestDebugREPL_ReadOnlyRefusesRunAndCall(t *testing.T) {
	cases := map[string]func(*debugSession) error{
		"run":  func(s *debugSession) error { return s.runProgram([]string{"ZDEMO_REPORT"}) },
		"call": func(s *debugSession) error { return s.callRFC([]string{"Z_DOUBLE", "N=21"}) },
	}
	for name, do := range cases {
		t.Run(name, func(t *testing.T) {
			s, hits := debugTestSession(t, true)
			err := do(s)
			if err == nil || !strings.Contains(err.Error(), "blocked by safety configuration") {
				t.Fatalf("want a safety refusal, got %v", err)
			}
			time.Sleep(200 * time.Millisecond) // a listener, had one started, would have called by now
			if n := hits(); n != 0 {
				t.Errorf("a refused %s still reached SAP %d time(s)", name, n)
			}
		})
	}
}

func TestDebugREPL_WritableRunIsNotRefused(t *testing.T) {
	s, _ := debugTestSession(t, false)
	err := s.runProgram([]string{"ZDEMO_REPORT"})
	if err != nil && strings.Contains(err.Error(), "blocked") {
		t.Fatalf("run refused on a writable system: %v", err)
	}
}

// cliReadOnly is what vsp debug and vsp rfc read: read_only or SAP_READ_ONLY.
func TestCLIReadOnly_ConfigOrEnvironment(t *testing.T) {
	t.Setenv("SAP_READ_ONLY", "")
	if cliReadOnly(&systemParams{}) {
		t.Error("read-only with neither set")
	}
	if !cliReadOnly(&systemParams{ReadOnly: true}) {
		t.Error("read_only ignored")
	}
	t.Setenv("SAP_READ_ONLY", "true")
	if !cliReadOnly(&systemParams{}) {
		t.Error("SAP_READ_ONLY ignored")
	}
}
