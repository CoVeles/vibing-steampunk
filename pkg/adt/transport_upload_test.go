package adt

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// A synthetic cofile of request XYZK900001, exported from XYZ, in the shape
// STRF_READ_COFILE reads.
const sampleCofile = "#$PROJECT = \n" +
	"TESTUSER K QAS 3 1 0 0 0 0 0 0 0 0\n" +
	"XYZ.100 E 0004 20260101120000 dev.example.local xyzadm\n" +
	"XYZ.100 R 0000 20260101120001 dev.example.local xyzadm\n"

// realCofile is a cofile SAP wrote on release (7.58), with the system, user
// and host replaced: XYZ exported to QAS. Its header runs past the nine
// object counts, and it has '#' lines after the header.
const realCofile = "TESTUSER     K QAS        1   1   2   0   0   0   0   0   0   2  758   .  0   0   0   0   0 001\n" +
	"#U\n" +
	"#/1/                          A   G   -   C   R   7   T   -   Z RELE EX.  _   _   _   _   _ CLI\n" +
	"XYZ f 0000 20261001165403 dev.example.local       xyzadm\n" +
	"XYZ e 0000 20261001165406 dev.example.local       xyzadm\n" +
	"QAS < 0000 20261001165410 dev.example.local       xyzadm\n" +
	"XYZ E 0000 20261001165410 dev.example.local       xyzadm\n"

func TestValidateCofileAcceptsWhatSAPWrites(t *testing.T) {
	if err := ValidateCofile([]byte(realCofile), "XYZ"); err != nil {
		t.Fatalf("a cofile SAP wrote is refused: %v", err)
	}
	if err := ValidateCofile([]byte(realCofile), "ABC"); err == nil {
		t.Error("the same cofile accepted for another source system")
	}
}

func sampleData() []byte { return []byte("\x00\x01binary R3trans payload\xff\xfe") }

func TestTransportRequestFromFileNames(t *testing.T) {
	req, sid, nr, err := TransportRequestFromFileNames("K900001.XYZ", "R900001.XYZ")
	if err != nil || req != "XYZK900001" || sid != "XYZ" || nr != "900001" {
		t.Fatalf("got %q %q %q %v", req, sid, nr, err)
	}
	bad := map[string][2]string{
		"no cofile":         {"", "R900001.XYZ"},
		"no data file":      {"K900001.XYZ", ""},
		"lowercase":         {"k900001.xyz", "r900001.xyz"},
		"five digits":       {"K90001.XYZ", "R90001.XYZ"},
		"seven digits":      {"K9000011.XYZ", "R9000011.XYZ"},
		"other number":      {"K900001.XYZ", "R900002.XYZ"},
		"other SID":         {"K900001.XYZ", "R900001.XYA"},
		"swapped":           {"R900001.XYZ", "K900001.XYZ"},
		"path separator":    {"cofiles/K900001.XYZ", "R900001.XYZ"},
		"dot dot":           {"../K900001.XYZ", "R900001.XYZ"},
		"NUL":               {"K900001.XYZ\x00", "R900001.XYZ"},
		"trailing newline":  {"K900001.XYZ\n", "R900001.XYZ"},
		"SID of four":       {"K900001.XYZW", "R900001.XYZW"},
		"data file D (SDO)": {"K900001.XYZ", "D900001.XYZ"},
	}
	for name, p := range bad {
		if _, _, _, err := TransportRequestFromFileNames(p[0], p[1]); err == nil {
			t.Errorf("%s: %q + %q accepted", name, p[0], p[1])
		}
	}
}

func TestTransportFileNamesForRequest(t *testing.T) {
	c, d, err := TransportFileNamesForRequest("xyzk900001")
	if err != nil || c != "K900001.XYZ" || d != "R900001.XYZ" {
		t.Fatalf("got %q %q %v", c, d, err)
	}
	for _, r := range []string{"", "XYZK90001", "XYZ900001", "XYZK900001X", "XY/K900001"} {
		if _, _, err := TransportFileNamesForRequest(r); err == nil {
			t.Errorf("%q accepted", r)
		}
	}
}

func TestValidateCofile(t *testing.T) {
	if err := ValidateCofile([]byte(sampleCofile), "XYZ"); err != nil {
		t.Fatalf("sample refused: %v", err)
	}
	if err := ValidateCofile([]byte(strings.ReplaceAll(sampleCofile, "\n", "\r\n")), "XYZ"); err != nil {
		t.Fatalf("CRLF sample refused: %v", err)
	}
	withPred := "#$PREDECESSOR = XYZK900000\n" + sampleCofile
	if err := ValidateCofile([]byte(withPred), "XYZ"); err != nil {
		t.Fatalf("directive lines refused: %v", err)
	}
	bad := map[string]string{
		"empty":                  "",
		"only directives":        "#$PROJECT = \n#$PREDECESSOR = XYZK900000\n",
		"header too short":       "TESTUSER K QAS\nXYZ E 0000 20260101120000 h u\n",
		"type not a letter":      "TESTUSER 7 QAS 3\nXYZ E 0000 20260101120000 h u\n",
		"step not 0-3":           "TESTUSER K QAS 9\nXYZ E 0000 20260101120000 h u\n",
		"count not a number":     "TESTUSER K QAS 3 x\nXYZ E 0000 20260101120000 h u\n",
		"no steps":               "TESTUSER K QAS 3 1 0\n",
		"bad step time":          "TESTUSER K QAS 3\nXYZ E 0000 2026 h u\n",
		"bad step retcode":       "TESTUSER K QAS 3\nXYZ E rc 20260101120000 h u\n",
		"no export":              "TESTUSER K QAS 3\nXYZ.100 R 0000 20260101120000 h u\n",
		"export from another":    "TESTUSER K QAS 3\nABC.100 E 0000 20260101120000 h u\n",
		"NUL":                    sampleCofile + "\x00",
		"control character":      sampleCofile + "\x07",
		"not UTF-8":              sampleCofile + "\xff",
		"data file given":        string(sampleData()),
		"function not one char":  "TESTUSER K QAS 3\nXYZ EX 0000 20260101120000 h u\n",
		"target with path chars": "TESTUSER K ../x 3\nXYZ E 0000 20260101120000 h u\n",
	}
	for name, c := range bad {
		if err := ValidateCofile([]byte(c), "XYZ"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	big := bytes.Repeat([]byte("x"), TransportCofileMaxBytes+1)
	if err := ValidateCofile(big, "XYZ"); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Errorf("oversize cofile: %v", err)
	}
}

func TestNewTransportFiles(t *testing.T) {
	f, err := NewTransportFiles("K900001.XYZ", []byte(sampleCofile), "R900001.XYZ", sampleData())
	if err != nil || f.Request != "XYZK900001" {
		t.Fatalf("%+v %v", f, err)
	}
	if _, err := NewTransportFiles("K900001.XYZ", []byte(sampleCofile), "R900001.XYZ", nil); err == nil {
		t.Error("empty data file accepted")
	}
	if _, err := NewTransportFiles("K900001.XYZ", nil, "R900001.XYZ", sampleData()); err == nil {
		t.Error("empty cofile accepted")
	}
	huge := make([]byte, TransportUploadMaxBytes-len(sampleCofile)+1)
	if _, err := NewTransportFiles("K900001.XYZ", []byte(sampleCofile), "R900001.XYZ", huge); err == nil || !strings.Contains(err.Error(), "50 MB") {
		t.Errorf("over 50 MB together: %v", err)
	}
	exact := make([]byte, TransportUploadMaxBytes-len(sampleCofile))
	if _, err := NewTransportFiles("K900001.XYZ", []byte(sampleCofile), "R900001.XYZ", exact); err != nil {
		t.Errorf("exactly 50 MB together refused: %v", err)
	}
}

func TestReadTransportFiles(t *testing.T) {
	dir := t.TempDir()
	co, da := filepath.Join(dir, "K900001.XYZ"), filepath.Join(dir, "R900001.XYZ")
	must(t, os.WriteFile(co, []byte(sampleCofile), 0o600))
	must(t, os.WriteFile(da, sampleData(), 0o600))
	f, err := ReadTransportFiles(co, da)
	if err != nil || f.Request != "XYZK900001" || !bytes.Equal(f.Data, sampleData()) {
		t.Fatalf("%+v %v", f, err)
	}

	if _, err := ReadTransportFiles(co, ""); err == nil {
		t.Error("missing data file path accepted")
	}
	if _, err := ReadTransportFiles(co, filepath.Join(dir, "R900002.XYZ")); err == nil {
		t.Error("unpaired files accepted")
	}

	// A symbolic link is refused, even one with the right name.
	ldir := t.TempDir()
	link := filepath.Join(ldir, "R900001.XYZ")
	must(t, os.Symlink(da, link))
	if _, err := ReadTransportFiles(co, link); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Errorf("symlink: %v", err)
	}
	// A directory is refused.
	ddir := t.TempDir()
	must(t, os.Mkdir(filepath.Join(ddir, "R900001.XYZ"), 0o700))
	if _, err := ReadTransportFiles(co, filepath.Join(ddir, "R900001.XYZ")); err == nil || !strings.Contains(err.Error(), "regular") {
		t.Errorf("directory: %v", err)
	}
	// An oversize file is refused before it is read.
	odir := t.TempDir()
	big := filepath.Join(odir, "R900001.XYZ")
	fh, err := os.Create(big)
	must(t, err)
	must(t, fh.Truncate(TransportUploadMaxBytes+1))
	must(t, fh.Close())
	if _, err := ReadTransportFiles(co, big); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Errorf("oversize: %v", err)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// --- gates -------------------------------------------------------------------

func uploadClient(s SafetyConfig) *Client {
	return NewClient("http://sap.invalid", "TESTUSER", "unused", WithClient("100"), WithSafety(s))
}

func enabled() SafetyConfig {
	s := UnrestrictedSafetyConfig()
	s.EnableTransports = true
	return s
}

func TestCheckTransportUploadGates(t *testing.T) {
	cases := map[string]struct {
		mod  func(*SafetyConfig)
		want string
	}{
		"read-only, transports enabled": {func(s *SafetyConfig) { s.ReadOnly = true }, "read-only"},
		"transports not enabled":        {func(s *SafetyConfig) { s.EnableTransports = false }, "not enabled"},
		"transport read-only":           {func(s *SafetyConfig) { s.TransportReadOnly = true }, "transport read-only"},
		"outside the whitelist":         {func(s *SafetyConfig) { s.AllowedTransports = []string{"ABCK*"} }, "allowed"},
		"transport ops disallowed":      {func(s *SafetyConfig) { s.DisallowedOps = "X" }, "blocked"},
	}
	for name, c := range cases {
		s := enabled()
		c.mod(&s)
		err := uploadClient(s).CheckTransportUpload("XYZK900001")
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want an error naming %q", name, err, c.want)
		}
	}
	s := enabled()
	s.AllowedTransports = []string{"XYZK9*"}
	if err := uploadClient(s).CheckTransportUpload("XYZK900001"); err != nil {
		t.Errorf("inside the whitelist: %v", err)
	}
	if err := uploadClient(enabled()).CheckTransportUpload("XYZK90001"); err == nil {
		t.Error("malformed request accepted")
	}
}

func TestCheckTransportBufferRead(t *testing.T) {
	s := UnrestrictedSafetyConfig()
	if err := uploadClient(s).CheckTransportBufferRead("", "TransportBuffer"); err == nil {
		t.Error("buffer read without --enable-transports accepted")
	}
	s = enabled()
	s.ReadOnly = true // a read stays possible under read-only
	if err := uploadClient(s).CheckTransportBufferRead("XYZK900001", "TransportBuffer"); err != nil {
		t.Errorf("read under read-only: %v", err)
	}
	s.AllowedTransports = []string{"ABCK*"}
	if err := uploadClient(s).CheckTransportBufferRead("XYZK900001", "TransportBuffer"); err == nil {
		t.Error("read outside the whitelist accepted")
	}
}

// --- the WebSocket conversation ----------------------------------------------

type wsCall struct {
	Action string
	Params map[string]any
}

// fakeTransportWS plays ZCL_VSP_TRANSPORT_SERVICE: it reassembles the chunks
// and answers like the class does. Hooks change single answers.
type fakeTransportWS struct {
	mu     sync.Mutex
	calls  []wsCall
	system string
	client string
	// beginRequest overrides the request begin reports.
	beginRequest string
	addErr       *WSError
	buffer       []map[string]any
	files        map[string][]byte // download source
	got          map[string][]byte
	// pending is the job add_to_buffer or show_buffer started; the first
	// buffer_result poll for it answers "pending".
	pending map[string]any
	polls   int
}

func init() { transportPollInterval = time.Millisecond }

func newFakeTransportWS() *fakeTransportWS {
	return &fakeTransportWS{system: "QAS", client: "100", got: map[string][]byte{}}
}

func (f *fakeTransportWS) SendDomainRequest(_ context.Context, domain, action string, params map[string]any, _ time.Duration) (*WSResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Round-trip the params through JSON, as the socket does.
	b, _ := json.Marshal(params)
	var p map[string]any
	_ = json.Unmarshal(b, &p)
	f.calls = append(f.calls, wsCall{Action: action, Params: p})
	if domain != "transport" {
		return &WSResponse{Success: false, Error: &WSError{Code: "UNKNOWN_DOMAIN", Message: "no"}}, nil
	}
	ok := func(v any) (*WSResponse, error) {
		raw, _ := json.Marshal(v)
		return &WSResponse{Success: true, Data: raw}, nil
	}
	switch action {
	case "upload_files":
		switch p["step"] {
		case "begin":
			req := "XYZK900001"
			if f.beginRequest != "" {
				req = f.beginRequest
			}
			return ok(map[string]any{"assembly_id": "A1", "request": req, "system": f.system, "client": f.client})
		case "chunk":
			chunk, _ := base64.StdEncoding.DecodeString(p["chunk_b64"].(string))
			k := p["file"].(string)
			if int(p["offset"].(float64)) != len(f.got[k]) {
				return &WSResponse{Success: false, Error: &WSError{Code: "INVALID_CHUNK", Message: "order"}}, nil
			}
			f.got[k] = append(f.got[k], chunk...)
			return ok(map[string]any{})
		case "commit":
			return ok(map[string]any{"request": "XYZK900001", "cofile_path": "/trans/cofiles/K900001.XYZ", "data_path": "/trans/data/R900001.XYZ"})
		case "abort":
			return ok(map[string]any{"aborted": true})
		}
	case "show_buffer":
		// A read of the buffer file: one message, one answer, no job.
		var entries []map[string]any
		for _, e := range f.buffer {
			if r, _ := p["request"].(string); r == "" || e["trkorr"] == r {
				entries = append(entries, e)
			}
		}
		return ok(map[string]any{"status": "done", "system": f.system, "client": f.client, "source": "DIR_TRANS/buffer/" + f.system,
			"file_exists": true, "total": len(entries), "entries": entries})
	case "add_to_buffer":
		p["action"] = action
		f.pending, f.polls = p, 0
		return ok(map[string]any{"status": "started", "ticket": "4711", "job": "ZVSP_TRANSPORT_BUFFER"})
	case "buffer_result":
		if f.pending == nil || p["ticket"] != "4711" {
			return &WSResponse{Success: false, Error: &WSError{Code: "NO_SUCH_JOB", Message: "none"}}, nil
		}
		if f.polls++; f.polls == 1 {
			return ok(map[string]any{"status": "pending", "job_status": "R"})
		}
		job := f.pending
		f.pending = nil
		if job["action"] == "add_to_buffer" {
			if f.addErr != nil {
				return &WSResponse{Success: false, Error: f.addErr}, nil
			}
			f.buffer = append(f.buffer, map[string]any{"trkorr": job["request"], "tarcli": ""})
			return ok(map[string]any{"status": "done", "request": job["request"], "system": f.system,
				"tp_command": "ADDTOBUFFER " + job["request"].(string) + " " + f.system, "tp_rc": "0000"})
		}
		return &WSResponse{Success: false, Error: &WSError{Code: "INVALID_PARAM", Message: "job action"}}, nil
	case "download_files":
		name := p["file"].(string)
		data := f.files[name]
		off, n := int(p["offset"].(float64)), int(p["length"].(float64))
		end := min(off+n, len(data))
		return ok(map[string]any{"name": name, "size": len(data), "offset": off, "chunk_b64": base64.StdEncoding.EncodeToString(data[off:end])})
	}
	return &WSResponse{Success: false, Error: &WSError{Code: "UNKNOWN_ACTION", Message: action}}, nil
}

func (f *fakeTransportWS) actions() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		a := c.Action
		if s, ok := c.Params["step"].(string); ok {
			a += ":" + s
		}
		out = append(out, a)
	}
	return out
}

func sampleFiles(t *testing.T, data []byte) *TransportFiles {
	t.Helper()
	f, err := NewTransportFiles("K900001.XYZ", []byte(sampleCofile), "R900001.XYZ", data)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestUploadTransportRefusesBeforeAnyMessage(t *testing.T) {
	for name, mod := range map[string]func(*SafetyConfig){
		"read-only":             func(s *SafetyConfig) { s.ReadOnly = true },
		"transports disabled":   func(s *SafetyConfig) { s.EnableTransports = false },
		"transport read-only":   func(s *SafetyConfig) { s.TransportReadOnly = true },
		"outside the whitelist": func(s *SafetyConfig) { s.AllowedTransports = []string{"ABCK*"} },
	} {
		s := enabled()
		mod(&s)
		ws := newFakeTransportWS()
		if _, err := uploadClient(s).UploadTransport(context.Background(), ws, sampleFiles(t, sampleData())); err == nil {
			t.Errorf("%s: upload accepted", name)
		}
		if len(ws.actions()) != 0 {
			t.Errorf("%s: sent %v before refusing", name, ws.actions())
		}
	}
	// Files that do not validate are refused before anything is sent too.
	ws := newFakeTransportWS()
	bad := &TransportFiles{CofileName: "K900001.XYZ", DataName: "R900002.XYZ", Cofile: []byte(sampleCofile), Data: sampleData()}
	if _, err := uploadClient(enabled()).UploadTransport(context.Background(), ws, bad); err == nil || len(ws.actions()) != 0 {
		t.Errorf("unpaired files: %v, sent %v", err, ws.actions())
	}
}

func TestUploadTransportHappyPath(t *testing.T) {
	data := bytes.Repeat([]byte{0, 1, 2, 3, 0xff}, transportUploadChunk/2) // spans three chunks
	files := sampleFiles(t, data)
	ws := newFakeTransportWS()
	res, err := uploadClient(enabled()).UploadTransport(context.Background(), ws, files)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"upload_files:begin", "upload_files:chunk", "upload_files:chunk", "upload_files:chunk",
		"upload_files:chunk", "upload_files:commit", "add_to_buffer", "buffer_result", "buffer_result",
		"show_buffer"}
	if got := ws.actions(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("conversation:\n got %v\nwant %v", got, want)
	}
	if !bytes.Equal(ws.got["data"], data) || string(ws.got["cofile"]) != sampleCofile {
		t.Error("the chunks do not reassemble into the files")
	}
	begin := ws.calls[0].Params
	if begin["data_sha256"] != hexSHA(data) || begin["cofile_sha256"] != hexSHA([]byte(sampleCofile)) ||
		int(begin["data_size"].(float64)) != len(data) || begin["cofile_name"] != "K900001.XYZ" {
		t.Errorf("begin declared %v", begin)
	}
	// The data file goes first: tp must never find a cofile without its data.
	if ws.calls[1].Params["file"] != "data" || ws.calls[4].Params["file"] != "cofile" {
		t.Error("the data file must be sent before the cofile")
	}
	// No call carries a directory, a system or a tp command.
	for _, c := range ws.calls {
		for _, k := range []string{"dir", "dir_name", "path", "system", "sid", "tp_command", "command", "client"} {
			if _, ok := c.Params[k]; ok {
				t.Errorf("%s carries %q", c.Action, k)
			}
		}
	}
	if !res.FilesWritten || !res.Queued || res.InBuffer == nil || res.System != "QAS" {
		t.Errorf("result %+v", res)
	}
	if !strings.Contains(res.Note, "NOT been imported") {
		t.Errorf("note %q", res.Note)
	}
}

func hexSHA(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func TestUploadTransportRefusesAnotherClient(t *testing.T) {
	ws := newFakeTransportWS()
	ws.client = "200"
	_, err := uploadClient(enabled()).UploadTransport(context.Background(), ws, sampleFiles(t, sampleData()))
	if err == nil || !strings.Contains(err.Error(), "client 200") {
		t.Fatalf("got %v", err)
	}
	if got := strings.Join(ws.actions(), ","); got != "upload_files:begin,upload_files:abort" {
		t.Errorf("conversation %s: nothing may be sent after begin but abort", got)
	}
}

func TestUploadTransportRefusesAnotherRequest(t *testing.T) {
	ws := newFakeTransportWS()
	ws.beginRequest = "XYZK900002"
	_, err := uploadClient(enabled()).UploadTransport(context.Background(), ws, sampleFiles(t, sampleData()))
	if err == nil || got(ws) != "upload_files:begin,upload_files:abort" {
		t.Fatalf("got %v, %s", err, got(ws))
	}
}

func got(ws *fakeTransportWS) string { return strings.Join(ws.actions(), ",") }

func TestUploadTransportAddFailure(t *testing.T) {
	ws := newFakeTransportWS()
	ws.addErr = &WSError{Code: "ADD_FAILED_ROLLED_BACK", Message: "tp failed. The files this upload wrote were deleted again."}
	res, err := uploadClient(enabled()).UploadTransport(context.Background(), ws, sampleFiles(t, sampleData()))
	var se *TransportServiceError
	if !errors.As(err, &se) || se.Code != "ADD_FAILED_ROLLED_BACK" {
		t.Fatalf("got %v", err)
	}
	if res == nil || res.Queued || res.FilesWritten || !res.RolledBack {
		t.Errorf("result %+v", res)
	}
	ws = newFakeTransportWS()
	ws.addErr = &WSError{Code: "PERMISSION_DENIED", Message: "needs S_CTS_ADMI TADD"}
	res, err = uploadClient(enabled()).UploadTransport(context.Background(), ws, sampleFiles(t, sampleData()))
	if err == nil || !strings.Contains(err.Error(), "TADD") || res.Queued || !res.FilesWritten {
		t.Errorf("permission: %v %+v", err, res)
	}
}

func TestTransportServiceMissing(t *testing.T) {
	ws := &unknownDomainWS{}
	_, err := uploadClient(enabled()).TransportBuffer(context.Background(), ws, "")
	if err == nil || !strings.Contains(err.Error(), "install zadt-vsp") {
		t.Errorf("got %v", err)
	}
}

type unknownDomainWS struct{}

func (unknownDomainWS) SendDomainRequest(context.Context, string, string, map[string]any, time.Duration) (*WSResponse, error) {
	return &WSResponse{Success: false, Error: &WSError{Code: "UNKNOWN_DOMAIN", Message: "Domain 'transport' not found"}}, nil
}

func TestTransportBufferAndDownload(t *testing.T) {
	ws := newFakeTransportWS()
	ws.buffer = []map[string]any{{"trkorr": "XYZK900001", "tarcli": "100"}, {"trkorr": "XYZK900003"}}
	buf, err := uploadClient(enabled()).TransportBuffer(context.Background(), ws, "xyzk900001")
	if err != nil || len(buf.Entries) != 1 || buf.Entries[0].Client != "100" {
		t.Fatalf("%+v %v", buf, err)
	}
	if _, ok := buf.Contains("XYZK900001"); !ok {
		t.Error("Contains")
	}
	// The view is a plain read: one show_buffer message, no job, no polling.
	if got := strings.Join(ws.actions(), ","); got != "show_buffer" {
		t.Errorf("a buffer view sent %s", got)
	}

	data := bytes.Repeat([]byte{7}, transportDownloadChunk+10)
	ws.files = map[string][]byte{"cofile": []byte(sampleCofile), "data": data}
	f, err := uploadClient(enabled()).DownloadTransportFiles(context.Background(), ws, "XYZK900001")
	if err != nil || f.CofileName != "K900001.XYZ" || !bytes.Equal(f.Data, data) || string(f.Cofile) != sampleCofile {
		t.Fatalf("%v", err)
	}
	// A download is a read: it is refused only without transports.
	if _, err := uploadClient(UnrestrictedSafetyConfig()).DownloadTransportFiles(context.Background(), ws, "XYZK900001"); err == nil {
		t.Error("download without --enable-transports accepted")
	}
}

// A buffer job that never reports is not taken for success or failure.
func TestTransportJobTimeout(t *testing.T) {
	saved := transportJobTimeout
	transportJobTimeout = 20 * time.Millisecond
	t.Cleanup(func() { transportJobTimeout = saved })
	ws := stuckJobWS{}
	err := transportJob(context.Background(), ws, "add_to_buffer", map[string]any{"request": "XYZK900001"}, &struct{}{})
	if err == nil || !strings.Contains(err.Error(), "SM37") || !strings.Contains(err.Error(), "unknown") {
		t.Errorf("got %v", err)
	}
}

type stuckJobWS struct{}

func (stuckJobWS) SendDomainRequest(_ context.Context, _, action string, _ map[string]any, _ time.Duration) (*WSResponse, error) {
	if action == "buffer_result" {
		return &WSResponse{Success: true, Data: []byte(`{"status":"pending","job_status":"S"}`)}, nil
	}
	return &WSResponse{Success: true, Data: []byte(`{"status":"started","ticket":"4711","job":"ZVSP_TRANSPORT_BUFFER"}`)}, nil
}

// A download is a sensitive read: refused under read-only, even with
// transports enabled, before anything is sent.
func TestDownloadTransportFilesRefusedUnderReadOnly(t *testing.T) {
	s := enabled()
	s.ReadOnly = true
	ws := newFakeTransportWS()
	if _, err := uploadClient(s).DownloadTransportFiles(context.Background(), ws, "XYZK900001"); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Errorf("got %v", err)
	}
	if len(ws.actions()) != 0 {
		t.Errorf("sent %v", ws.actions())
	}
}
