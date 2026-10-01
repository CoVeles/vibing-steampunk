package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/oisee/vibing-steampunk/pkg/adt"
)

// A synthetic cofile of XYZK900001, exported from XYZ.
const uploadTestCofile = "TESTUSER K QAS 3 1 0 0 0 0 0 0 0 0\n" +
	"XYZ.100 E 0004 20260101120000 dev.example.local xyzadm\n"

// uploadFakeWS answers ZADT_VSP's transport domain and records every call.
type uploadFakeWS struct {
	mu      sync.Mutex
	actions []string
	dials   int
	client  string
}

func (f *uploadFakeWS) SendDomainRequest(_ context.Context, domain, action string, params map[string]any, _ time.Duration) (*adt.WSResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if step, ok := params["step"].(string); ok {
		action += ":" + step
	}
	f.actions = append(f.actions, domain+"."+action)
	var data any
	switch action {
	case "upload_files:begin":
		data = map[string]any{"assembly_id": "A1", "request": "XYZK900001", "system": "QAS", "client": f.client}
	case "add_to_buffer":
		data = map[string]any{"request": "XYZK900001", "system": "QAS", "tp_command": "ADDTOBUFFER XYZK900001 QAS", "tp_rc": "0000"}
	case "show_buffer":
		data = map[string]any{"system": "QAS", "client": f.client, "total": 1, "entries": []any{map[string]any{"trkorr": "XYZK900001"}}}
	default:
		data = map[string]any{}
	}
	raw, _ := json.Marshal(data)
	return &adt.WSResponse{Success: true, Data: raw}, nil
}

func (f *uploadFakeWS) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.actions...)
}

func (f *uploadFakeWS) dialed() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.dials
}

// uploadServer is a server over the fake; cfg adjusts its configuration.
func uploadServer(t *testing.T, cfg func(*Config)) (*Server, *uploadFakeWS) {
	t.Helper()
	t.Chdir(t.TempDir()) // no .vsp.json unless the test writes one
	c := &Config{
		BaseURL: "https://qas.example.local:44300", Client: "100", Username: "TESTUSER", Password: "unused",
		Mode: "expert", EnableTransports: true,
	}
	if cfg != nil {
		cfg(c)
	}
	s := NewServer(c)
	ws := &uploadFakeWS{client: "100"}
	s.transportWS = func(context.Context) (adt.TransportService, error) {
		ws.mu.Lock()
		ws.dials++
		ws.mu.Unlock()
		return ws, nil
	}
	return s, ws
}

// uploadFiles writes the two files and returns their paths.
func uploadFiles(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	co, da := filepath.Join(dir, "K900001.XYZ"), filepath.Join(dir, "R900001.XYZ")
	if err := os.WriteFile(co, []byte(uploadTestCofile), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(da, []byte{0, 1, 2, 3}, 0o600); err != nil {
		t.Fatal(err)
	}
	return co, da
}

func callUpload(t *testing.T, s *Server, params map[string]any) *mcp.CallToolResult {
	t.Helper()
	p := map[string]any{"type": "upload_transport"}
	for k, v := range params {
		p[k] = v
	}
	res, err := s.handleUniversalTool(context.Background(), newRequest(map[string]any{"action": "system", "params": p}))
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func uploadResultText(r *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range r.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// Every gate refuses before a file is read or ZADT_VSP is dialled.
func TestUploadTransportGates(t *testing.T) {
	co, da := uploadFiles(t)
	paths := map[string]any{"cofile_path": co, "datafile_path": da}
	cases := map[string]struct {
		cfg    func(*Config)
		params map[string]any
		want   string
	}{
		"--read-only, even with --enable-transports": {func(c *Config) { c.ReadOnly = true }, paths, "read-only"},
		"no --enable-transports":                     {func(c *Config) { c.EnableTransports = false }, paths, "enable-transports"},
		"--transport-read-only":                      {func(c *Config) { c.TransportReadOnly = true }, paths, "transport read-only"},
		"--allowed-transports does not cover it":     {func(c *Config) { c.AllowedTransports = []string{"ABCK*"} }, paths, "allowed"},
		"focused mode":                               {func(c *Config) { c.Mode = "focused" }, paths, "expert mode only"},
		"hyperfocused mode":                          {func(c *Config) { c.Mode = "hyperfocused" }, paths, "expert mode only"},
		"another client named":                       {nil, map[string]any{"cofile_path": co, "datafile_path": da, "client": "200"}, "own system"},
		"another system named":                       {nil, map[string]any{"cofile_path": co, "datafile_path": da, "system": "PRD"}, "own system"},
		"a directory named":                          {nil, map[string]any{"cofile_path": co, "datafile_path": da, "dir": "/tmp"}, "own system"},
		"no files":                                   {nil, map[string]any{}, "both files"},
		"one file":                                   {nil, map[string]any{"cofile_path": co}, "both files"},
		"unpaired files":                             {nil, map[string]any{"cofile_path": co, "datafile_path": filepath.Join(filepath.Dir(da), "R900002.XYZ")}, "not one request"},
		"wrong name pattern":                         {nil, map[string]any{"cofile_path": filepath.Join(filepath.Dir(co), "cofile.txt"), "datafile_path": da}, "K<6 digits>"},
		"paths and contents both":                    {nil, map[string]any{"cofile_path": co, "datafile_path": da, "cofile_base64": "eA==", "datafile_base64": "eA=="}, "not both"},
		"cofile that is not one": {nil, map[string]any{"cofile_name": "K900001.XYZ", "cofile_base64": base64.StdEncoding.EncodeToString([]byte("hello")),
			"datafile_name": "R900001.XYZ", "datafile_base64": "AAE="}, "header"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			s, ws := uploadServer(t, c.cfg)
			res := callUpload(t, s, c.params)
			if !res.IsError || !strings.Contains(uploadResultText(res), c.want) {
				t.Errorf("got %q, want an error naming %q", uploadResultText(res), c.want)
			}
			if ws.dialed() != 0 || len(ws.calls()) != 0 {
				t.Errorf("ZADT_VSP was dialled (%d) or sent %v before the refusal", ws.dialed(), ws.calls())
			}
		})
	}
}

// A .vsp.json entry named by -s that is another system than the one the
// server is connected to is refused, as it is for RFC.
func TestUploadTransportRefusesNamedSystemMismatch(t *testing.T) {
	co, da := uploadFiles(t)
	s, ws := uploadServer(t, func(c *Config) { c.SystemName = "prodsys-a" })
	if err := os.WriteFile(".vsp.json", []byte(`{"systems":{"prodsys-a":{"url":"https://prodsys-a.example:44300","client":"100"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	res := callUpload(t, s, map[string]any{"cofile_path": co, "datafile_path": da})
	if !res.IsError || !strings.Contains(uploadResultText(res), "prodsys-a") || ws.dialed() != 0 {
		t.Errorf("got %q, dials %d", uploadResultText(res), ws.dialed())
	}
}

func TestUploadTransportHappyPath(t *testing.T) {
	co, da := uploadFiles(t)
	s, ws := uploadServer(t, func(c *Config) { c.AllowedTransports = []string{"XYZK9*"} })
	res := callUpload(t, s, map[string]any{"cofile_path": co, "datafile_path": da})
	if res.IsError {
		t.Fatalf("refused: %s", uploadResultText(res))
	}
	want := "transport.upload_files:begin,transport.upload_files:chunk,transport.upload_files:chunk," +
		"transport.upload_files:commit,transport.add_to_buffer,transport.show_buffer"
	if got := strings.Join(ws.calls(), ","); got != want {
		t.Errorf("conversation:\n got %s\nwant %s", got, want)
	}
	if !strings.Contains(uploadResultText(res), "NOT been imported") || !strings.Contains(uploadResultText(res), `"queued": true`) {
		t.Errorf("answer %s", uploadResultText(res))
	}
}

func TestUploadTransportBase64(t *testing.T) {
	s, ws := uploadServer(t, nil)
	res := callUpload(t, s, map[string]any{
		"cofile_name": "K900001.XYZ", "cofile_base64": base64.StdEncoding.EncodeToString([]byte(uploadTestCofile)),
		"datafile_name": "R900001.XYZ", "datafile_base64": base64.StdEncoding.EncodeToString([]byte{9, 9}),
	})
	if res.IsError || len(ws.calls()) == 0 {
		t.Fatalf("refused: %s", uploadResultText(res))
	}
}

// The server's answering client must be its own: a mismatch stops the upload
// after begin, with nothing written.
func TestUploadTransportRefusesAnotherAnsweringClient(t *testing.T) {
	co, da := uploadFiles(t)
	s, ws := uploadServer(t, nil)
	ws.client = "200"
	res := callUpload(t, s, map[string]any{"cofile_path": co, "datafile_path": da})
	if !res.IsError || !strings.Contains(uploadResultText(res), "client 200") {
		t.Fatalf("got %s", uploadResultText(res))
	}
	if got := strings.Join(ws.calls(), ","); got != "transport.upload_files:begin,transport.upload_files:abort" {
		t.Errorf("conversation %s", got)
	}
}

func TestTransportBufferView(t *testing.T) {
	// A read: allowed under --read-only, but it needs --enable-transports.
	s, ws := uploadServer(t, func(c *Config) { c.ReadOnly = true; c.Mode = "hyperfocused" })
	res, err := s.handleUniversalTool(context.Background(), newRequest(map[string]any{
		"action": "system", "params": map[string]any{"type": "transport_buffer", "transport": "XYZK900001"}}))
	if err != nil || res.IsError || !strings.Contains(uploadResultText(res), "XYZK900001") {
		t.Fatalf("got %v %s", err, uploadResultText(res))
	}
	if got := strings.Join(ws.calls(), ","); got != "transport.show_buffer" {
		t.Errorf("a buffer view sent %s", got)
	}
	s, ws = uploadServer(t, func(c *Config) { c.EnableTransports = false })
	res, _ = s.handleUniversalTool(context.Background(), newRequest(map[string]any{
		"action": "system", "params": map[string]any{"type": "transport_buffer"}}))
	if !res.IsError || ws.dialed() != 0 {
		t.Errorf("without --enable-transports: %s, dials %d", uploadResultText(res), ws.dialed())
	}
}
