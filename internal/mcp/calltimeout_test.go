package mcp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// waitForCtx is a long call that only ends when its context does, failing
// the way the ADT client does then.
func waitForCtx(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	<-ctx.Done()
	return newToolResultError("execution failed: " + ctx.Err().Error()), nil
}

func TestLongCallBudgetFromParams(t *testing.T) {
	s := &Server{config: &Config{}}
	res, err := s.longCall(context.Background(), newRequest(map[string]any{"timeout": 0.05}), "execute_abap", waitForCtx)
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(res)
	if !res.IsError || !strings.Contains(text, "execute_abap timed out after 50ms") ||
		!strings.Contains(text, "may still be running on SAP") {
		t.Fatalf("want a timeout message, got %q", text)
	}
	if !strings.Contains(text, "Detail: execution failed: context deadline exceeded") {
		t.Fatalf("the handler's own text must be kept as detail, got %q", text)
	}
}

func TestLongCallServerDefaultBudget(t *testing.T) {
	s := &Server{config: &Config{CallTimeout: 50 * time.Millisecond}}
	done := make(chan *mcp.CallToolResult, 1)
	go func() {
		res, _ := s.longCall(context.Background(), newRequest(map[string]any{}), "deploy_zip", waitForCtx)
		done <- res
	}()
	select {
	case res := <-done:
		if !strings.Contains(resultText(res), "deploy_zip timed out after 50ms") {
			t.Fatalf("got %q", resultText(res))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the server default budget did not end the call")
	}
}

func TestLongCallClientCancel(t *testing.T) {
	s := &Server{config: &Config{}}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	res, _ := s.longCall(ctx, newRequest(map[string]any{}), "ABAP Unit run", waitForCtx)
	if text := resultText(res); !strings.Contains(text, "ABAP Unit run was cancelled by the client after") ||
		!strings.Contains(text, "may still be running on SAP") {
		t.Fatalf("got %q", text)
	}
}

func TestLongCallPerRequestTimeout(t *testing.T) {
	s := &Server{config: &Config{}}
	h := func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return nil, errors.New(`Post "https://sap/sap/bc/adt/abapunit/testruns": context deadline exceeded (Client.Timeout exceeded while awaiting headers)`)
	}
	res, err := s.longCall(context.Background(), newRequest(map[string]any{}), "ABAP Unit run", h)
	if err != nil {
		t.Fatalf("want the error turned into a result, got %v", err)
	}
	if text := resultText(res); !strings.Contains(text, "ABAP Unit run timed out after") ||
		!strings.Contains(text, "per-request limit") || !strings.Contains(text, "params.timeout") {
		t.Fatalf("got %q", text)
	}
}

func TestLongCallLeavesOtherOutcomesAlone(t *testing.T) {
	s := &Server{config: &Config{}}
	ok := func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText("fine"), nil
	}
	res, _ := s.longCall(context.Background(), newRequest(map[string]any{"timeout": float64(30)}), "x", ok)
	if res.IsError || resultText(res) != "fine" {
		t.Fatalf("got %+v", res)
	}
	failed := func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return newToolResultError("syntax error in line 3"), nil
	}
	res, _ = s.longCall(context.Background(), newRequest(map[string]any{}), "x", failed)
	if resultText(res) != "syntax error in line 3" {
		t.Fatalf("an ordinary failure must pass through, got %q", resultText(res))
	}
	for _, bad := range []any{float64(0), float64(-5), "soon"} {
		res, _ = s.longCall(context.Background(), newRequest(map[string]any{"timeout": bad}), "x", ok)
		if !res.IsError || !strings.Contains(resultText(res), "timeout must be") {
			t.Fatalf("timeout=%v: got %q", bad, resultText(res))
		}
	}
}

// slowSAP answers every request after delay.
func slowSAP(t *testing.T, delay time.Duration) *Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
		w.Header().Set("X-CSRF-Token", "t")
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(fakeEmptyXML))
	}))
	t.Cleanup(ts.Close)
	return NewServer(&Config{BaseURL: ts.URL, Username: "u", Password: "p", Client: "001", Language: "EN", Mode: "hyperfocused"})
}

// The long calls are wired through longCall, from the universal tool too.
func TestLongCallsHonourTimeoutParam(t *testing.T) {
	s := slowSAP(t, 3*time.Second)
	cases := []struct {
		name   string
		params map[string]any
		action string
		target string
		op     string
	}{
		{"execute_abap", map[string]any{"type": "execute_abap", "code": "lv_result = 1.", "timeout": 0.2}, "analyze", "", "execute_abap timed out"},
		{"unit tests", map[string]any{"object_url": "/sap/bc/adt/oo/classes/zcl_x", "timeout": 0.2}, "test", "", "ABAP Unit run timed out"},
	}
	file := filepath.Join(t.TempDir(), "zdemo_long.prog.abap")
	if err := os.WriteFile(file, []byte("REPORT zdemo_long.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases = append(cases, struct {
		name   string
		params map[string]any
		action string
		target string
		op     string
	}{"deploy_from_file", map[string]any{"type": "deploy_from_file", "file_path": file, "package_name": "$TMP", "timeout": 0.2}, "system", "", "deploy_from_file timed out"})
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			start := time.Now()
			res, err := s.handleUniversalTool(context.Background(), newRequest(map[string]any{
				"action": c.action, "target": c.target, "params": c.params,
			}))
			if err != nil {
				t.Fatal(err)
			}
			if time.Since(start) > 2*time.Second {
				t.Fatalf("the call ran %s; its 0.2s budget did not end it", time.Since(start))
			}
			if text := resultText(res); !strings.Contains(text, c.op) {
				t.Fatalf("got %q", text)
			}
		})
	}
}

// A client shutting the stdio session down, by closing stdin or by a signal,
// is a clean exit.
func TestServeStdioShutdownIsClean(t *testing.T) {
	s := NewServer(&Config{BaseURL: "http://127.0.0.1:1", Username: "u", Password: "p", Client: "001", Language: "EN", Mode: "hyperfocused"})

	if err := s.serveStdio(context.Background(), strings.NewReader(""), io.Discard); err != nil {
		t.Fatalf("stdin closed: got %v, want a clean exit", err)
	}

	pr, pw := io.Pipe()
	defer pw.Close()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	if err := s.serveStdio(ctx, pr, io.Discard); err != nil {
		t.Fatalf("signalled: got %v, want a clean exit", err)
	}
}
