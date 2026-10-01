package adt

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- a synthetic abapGit zip ------------------------------------------------

func dotAbapgitXML(start, logic string) string {
	return `<?xml version="1.0" encoding="utf-8"?>
<asx:abap xmlns:asx="http://www.sap.com/abapxml" version="1.0">
 <asx:values>
  <DATA>
   <MASTER_LANGUAGE>E</MASTER_LANGUAGE>
   <STARTING_FOLDER>` + start + `</STARTING_FOLDER>
   <FOLDER_LOGIC>` + logic + `</FOLDER_LOGIC>
  </DATA>
 </asx:values>
</asx:abap>`
}

// makeZip builds a zip of name -> content; an empty dot leaves .abapgit.xml out.
func makeZip(t *testing.T, dot string, files map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	add := func(n, c string) {
		f, err := w.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.Write([]byte(c))
	}
	if dot != "" {
		add(".abapgit.xml", dot)
	}
	for n, c := range files {
		add(n, c)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func demoZip(t *testing.T, logic string) []byte {
	return makeZip(t, dotAbapgitXML("/src/", logic), map[string]string{
		"README.md":                      "not an object",
		"src/package.devc.xml":           "<x/>",
		"src/zdemo_report.prog.abap":     "REPORT zdemo_report.",
		"src/zdemo_report.prog.xml":      "<x/>",
		"src/sub/package.devc.xml":       "<x/>",
		"src/sub/zcl_demo.clas.abap":     "CLASS zcl_demo DEFINITION. ENDCLASS.",
		"src/sub/zcl_demo.clas.xml":      "<x/>",
		"src/sub/#demo#cl_ns.clas.abap":  "CLASS /demo/cl_ns DEFINITION. ENDCLASS.",
		"src/sub/deep/package.devc.xml":  "<x/>",
		"src/sub/deep/zdemo_if.intf.xml": "<x/>",
	})
}

func TestAnalyzeGitZipFolderLogics(t *testing.T) {
	for logic, want := range map[string][]string{
		"PREFIX": {"$ZDEMO", "$ZDEMO_SUB", "$ZDEMO_SUB_DEEP"},
		"MIXED":  {"$ZDEMO", "$ZDEMO_DEEP", "$ZDEMO_SUB"},
		"FULL":   {"$ZDEMO", "$DEEP", "$SUB"},
	} {
		plan, err := AnalyzeGitZip(demoZip(t, logic), "$zdemo")
		if err != nil {
			t.Fatalf("%s: %v", logic, err)
		}
		if strings.Join(plan.Packages, ",") != strings.Join(want, ",") {
			t.Errorf("%s: packages %v, want %v", logic, plan.Packages, want)
		}
	}
	plan, err := AnalyzeGitZip(demoZip(t, "PREFIX"), "$ZDEMO")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, o := range plan.Objects {
		got = append(got, o.Type+" "+o.Name+" "+o.Package)
	}
	want := []string{
		"CLAS /DEMO/CL_NS $ZDEMO_SUB", "CLAS ZCL_DEMO $ZDEMO_SUB",
		"DEVC $ZDEMO $ZDEMO", "DEVC $ZDEMO_SUB $ZDEMO_SUB", "DEVC $ZDEMO_SUB_DEEP $ZDEMO_SUB_DEEP",
		"INTF ZDEMO_IF $ZDEMO_SUB_DEEP", "PROG ZDEMO_REPORT $ZDEMO",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("objects:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	data := demoZip(t, "PREFIX")
	p2, _ := AnalyzeGitZip(data, "$ZDEMO")
	h := sha256.Sum256(data)
	if p2.SHA256 != hex.EncodeToString(h[:]) || p2.Size != len(data) {
		t.Error("the plan does not carry the zip's size and SHA-256")
	}
}

func TestAnalyzeGitZipRefuses(t *testing.T) {
	cases := map[string][]byte{
		"no .abapgit.xml":   makeZip(t, "", map[string]string{"src/zdemo.prog.abap": "x"}),
		"not a zip":         []byte("PK? no"),
		"bad folder logic":  makeZip(t, dotAbapgitXML("/src/", "FLAT"), nil),
		"bad start folder":  makeZip(t, dotAbapgitXML("src", "PREFIX"), nil),
		"dot dot":           makeZip(t, dotAbapgitXML("/src/", "PREFIX"), map[string]string{"src/../x.prog.abap": "x"}),
		"package too long":  makeZip(t, dotAbapgitXML("/src/", "PREFIX"), map[string]string{"src/averyveryverylongfoldername/x.prog.abap": "x"}),
		"bad package chars": makeZip(t, dotAbapgitXML("/src/", "FULL"), map[string]string{"src/a-b/x.prog.abap": "x"}),
		"empty":             {},
	}
	for name, data := range cases {
		if _, err := AnalyzeGitZip(data, "$ZDEMO"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := AnalyzeGitZip(demoZip(t, "PREFIX"), "$Z DEMO"); err == nil {
		t.Error("a package name with a blank accepted")
	}
	big := make([]byte, GitZipMaxBytes+1)
	if _, err := AnalyzeGitZip(big, "$ZDEMO"); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Errorf("over the limit: %v", err)
	}
}

// --- gates -------------------------------------------------------------------

func TestCheckGitImportPolicy(t *testing.T) {
	cases := []struct {
		name      string
		opts      []Option
		pkg, tr   string
		wantError string
	}{
		{"read-only", []Option{WithReadOnly()}, "$ZDEMO", "", "read-only"},
		{"create disallowed", []Option{WithSafety(SafetyConfig{DisallowedOps: "C"})}, "$ZDEMO", "", "blocked"},
		{"activate disallowed", []Option{WithSafety(SafetyConfig{DisallowedOps: "A"})}, "$ZDEMO", "", "blocked"},
		{"package outside whitelist", []Option{WithAllowedPackages("$ZOTHER")}, "$ZDEMO", "", "blocked by safety"},
		{"transportable without opt-in", nil, "ZDEMO", "TRXK900001", "transportable"},
		{"transportable, transport outside whitelist", []Option{WithAllowTransportableEdits(), WithAllowedTransports("ABCK*")}, "ZDEMO", "TRXK900001", "allowed transports"},
		{"local with a transport", nil, "$ZDEMO", "TRXK900001", "takes no transport"},
		{"malformed transport", []Option{WithAllowTransportableEdits()}, "ZDEMO", "TR-1", "not <SID>K"},
		{"local ok", []Option{WithAllowedPackages("$Z*")}, "$ZDEMO", "", ""},
		{"transportable ok", []Option{WithAllowTransportableEdits()}, "ZDEMO", "TRXK900001", ""},
	}
	for _, c := range cases {
		cl := NewClient("http://sap.invalid", "TESTUSER", "pw", c.opts...)
		err := cl.CheckGitImportPolicy(c.pkg, c.tr)
		switch {
		case c.wantError == "" && err != nil:
			t.Errorf("%s: %v", c.name, err)
		case c.wantError != "" && (err == nil || !strings.Contains(err.Error(), c.wantError)):
			t.Errorf("%s: got %v, want %q", c.name, err, c.wantError)
		}
	}
}

// Every package the zip maps a file to is checked, not only the target: a
// subfolder must not carry objects into a package the server may not touch.
func TestCheckGitImportPlanChecksEveryPackage(t *testing.T) {
	plan, err := AnalyzeGitZip(demoZip(t, "PREFIX"), "$ZDEMO")
	if err != nil {
		t.Fatal(err)
	}
	cl := NewClient("http://sap.invalid", "TESTUSER", "pw", WithAllowedPackages("$ZDEMO", "$ZDEMO_SUB"))
	if err := cl.CheckGitImportPlan(plan); err == nil || !strings.Contains(err.Error(), "$ZDEMO_SUB_DEEP") {
		t.Errorf("a subpackage outside the whitelist passed: %v", err)
	}
	cl = NewClient("http://sap.invalid", "TESTUSER", "pw", WithAllowedPackages("$ZDEMO*"))
	if err := cl.CheckGitImportPlan(plan); err != nil {
		t.Errorf("all packages allowed, refused: %v", err)
	}
	full, _ := AnalyzeGitZip(demoZip(t, "FULL"), "$ZDEMO")
	if err := cl.CheckGitImportPlan(full); err == nil {
		t.Error("FULL logic maps to $SUB and $DEEP, outside $ZDEMO*; accepted")
	}
}

// --- the git domain, faked ----------------------------------------------------

type fakeGitWS struct {
	mu      sync.Mutex
	calls   []wsCall
	client  string
	pkgName string
	// assembled zip
	got []byte
	// package_objects answers, in turn (the last one repeats)
	contents []map[string]any
	// delete_repo
	repoErr *WSError
	// import_status answers, in turn
	status []map[string]any
	// beginPackage overrides the package begin reports.
	beginPackage string
}

func (f *fakeGitWS) SendDomainRequest(_ context.Context, domain, action string, params map[string]any, _ time.Duration) (*WSResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, _ := json.Marshal(params)
	var p map[string]any
	_ = json.Unmarshal(b, &p)
	f.calls = append(f.calls, wsCall{Action: action, Params: p})
	if domain != "git" {
		return &WSResponse{Success: false, Error: &WSError{Code: "UNKNOWN_DOMAIN", Message: "no"}}, nil
	}
	ok := func(v any) (*WSResponse, error) {
		raw, _ := json.Marshal(v)
		return &WSResponse{Success: true, Data: raw}, nil
	}
	switch action {
	case "import_zip":
		switch p["step"] {
		case "begin":
			pkg := p["package"].(string)
			if f.beginPackage != "" {
				pkg = f.beginPackage
			}
			return ok(map[string]any{"assembly_id": "A1", "package": pkg, "system": "XYZ", "client": orDefaultString(f.client, "001")})
		case "chunk":
			c, _ := base64.StdEncoding.DecodeString(p["chunk_b64"].(string))
			if int(p["offset"].(float64)) != len(f.got) {
				return &WSResponse{Success: false, Error: &WSError{Code: "INVALID_CHUNK", Message: "order"}}, nil
			}
			f.got = append(f.got, c...)
			return ok(map[string]any{"received": len(f.got)})
		case "commit":
			return ok(map[string]any{"status": "pending", "job": "ZVSP_GIT_IMPORT", "job_count": "12345678", "package": "$ZDEMO"})
		case "abort":
			return ok(map[string]any{"aborted": true})
		}
	case "import_status":
		if len(f.status) == 0 {
			return ok(map[string]any{"outcome": "unknown"})
		}
		s := f.status[0]
		if len(f.status) > 1 {
			f.status = f.status[1:]
		}
		return ok(s)
	case "package_objects":
		if len(f.contents) == 0 {
			return ok(map[string]any{"package": p["package"], "exists": false})
		}
		c := f.contents[0]
		if len(f.contents) > 1 {
			f.contents = f.contents[1:]
		}
		return ok(c)
	case "delete_repo":
		if f.repoErr != nil {
			return &WSResponse{Success: false, Error: f.repoErr}, nil
		}
		return ok(map[string]any{"deleted": true, "key": "000000000001", "name": "demo", "package": p["package"]})
	}
	return &WSResponse{Success: false, Error: &WSError{Code: "GIT_ERROR", Message: "Unknown action: " + action}}, nil
}

func (f *fakeGitWS) actions() []string {
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

func (f *fakeGitWS) params(action string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		a := c.Action
		if s, ok := c.Params["step"].(string); ok {
			a += ":" + s
		}
		if a == action {
			return c.Params
		}
	}
	return nil
}

func TestStartGitImportSendsTheZipInChunks(t *testing.T) {
	var big bytes.Buffer
	for i := 0; big.Len() < 3*gitUploadChunk; i++ {
		fmt.Fprintf(&big, "WRITE / 'line %d'.\n", i)
	}
	// Stored, not deflated, so the zip is as large as its content.
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	f, _ := w.Create(".abapgit.xml")
	_, _ = f.Write([]byte(dotAbapgitXML("/src/", "PREFIX")))
	h := &zip.FileHeader{Name: "src/zdemo_big.prog.abap", Method: zip.Store}
	f, _ = w.CreateHeader(h)
	_, _ = f.Write(big.Bytes())
	_ = w.Close()
	data := b.Bytes()

	ws := &fakeGitWS{}
	cl := NewClient("http://sap.invalid", "TESTUSER", "pw", WithAllowedPackages("$ZDEMO"))
	started, err := cl.StartGitImport(context.Background(), ws, data, GitImportOptions{Package: "$zdemo", Overwrite: true})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ws.got, data) {
		t.Fatalf("the system assembled %d bytes, sent %d", len(ws.got), len(data))
	}
	acts := ws.actions()
	if acts[0] != "import_zip:begin" || acts[len(acts)-1] != "import_zip:commit" || len(acts) < 5 {
		t.Errorf("actions %v", acts)
	}
	begin := ws.params("import_zip:begin")
	sum := sha256.Sum256(data)
	if begin["sha256"] != hex.EncodeToString(sum[:]) || int(begin["size"].(float64)) != len(data) {
		t.Errorf("begin does not declare the zip's size and SHA-256: %v", begin)
	}
	if begin["package"] != "$ZDEMO" || begin["packages"] != "$ZDEMO" || begin["overwrite"] != "true" || begin["repo_name"] != "$ZDEMO" {
		t.Errorf("begin params %v", begin)
	}
	if started.JobCount != "12345678" || started.Job != "ZVSP_GIT_IMPORT" {
		t.Errorf("started %+v", started)
	}
}

// The gates run before anything is sent.
func TestStartGitImportGatesBeforeIO(t *testing.T) {
	zipData := demoZip(t, "PREFIX")
	cases := map[string]struct {
		opts []Option
		imp  GitImportOptions
	}{
		"read-only":                  {[]Option{WithReadOnly()}, GitImportOptions{Package: "$ZDEMO"}},
		"target outside whitelist":   {[]Option{WithAllowedPackages("$ZOTHER*")}, GitImportOptions{Package: "$ZDEMO"}},
		"subpackage outside":         {[]Option{WithAllowedPackages("$ZDEMO", "$ZDEMO_SUB")}, GitImportOptions{Package: "$ZDEMO"}},
		"transportable, no opt-in":   {nil, GitImportOptions{Package: "ZDEMO", Transport: "TRXK900001"}},
		"transportable, bad request": {[]Option{WithAllowTransportableEdits(), WithAllowedTransports("ABCK*")}, GitImportOptions{Package: "ZDEMO", Transport: "TRXK900001"}},
		"control char in repo name":  {nil, GitImportOptions{Package: "$ZDEMO", RepoName: "a\nb"}},
	}
	for name, c := range cases {
		ws := &fakeGitWS{}
		cl := NewClient("http://sap.invalid", "TESTUSER", "pw", c.opts...)
		if _, err := cl.StartGitImport(context.Background(), ws, zipData, c.imp); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if n := len(ws.actions()); n != 0 {
			t.Errorf("%s: %d messages sent before the refusal", name, n)
		}
	}
}

// ZADT_VSP answering from another client, or reading another package, aborts
// before a byte of the zip is sent.
func TestStartGitImportAbortsOnMismatch(t *testing.T) {
	for name, ws := range map[string]*fakeGitWS{
		"client":  {client: "200"},
		"package": {beginPackage: "$ZOTHER"},
	} {
		cl := NewClient("http://sap.invalid", "TESTUSER", "pw", WithClient("001"))
		if _, err := cl.StartGitImport(context.Background(), ws, demoZip(t, "PREFIX"), GitImportOptions{Package: "$ZDEMO"}); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if got := strings.Join(ws.actions(), ","); got != "import_zip:begin,import_zip:abort" {
			t.Errorf("%s: actions %s", name, got)
		}
	}
}

func TestGitImportStatusParsesTheResult(t *testing.T) {
	ws := &fakeGitWS{status: []map[string]any{
		{"job": "ZVSP_GIT_IMPORT", "job_count": "12345678", "job_found": true, "job_status": "R", "outcome": "pending"},
		{"job": "ZVSP_GIT_IMPORT", "job_count": "12345678", "job_found": true, "job_status": "F", "outcome": "done",
			"job_log": []string{"VSP package=$ZDEMO outcome=imported"},
			"result": map[string]any{
				"outcome": "imported_with_errors", "package": "$ZDEMO", "repo_key": "000000000007", "repo_name": "demo",
				"repo_created": true, "package_created": true, "info_count": 3,
				"log":       []map[string]any{{"type": "E", "text": "Syntax error", "obj_type": "PROG", "obj_name": "ZDEMO_REPORT"}},
				"tadir":     []map[string]any{{"pgmid": "R3TR", "object": "PROG", "obj_name": "ZDEMO_REPORT", "devclass": "$ZDEMO", "created": true}},
				"decisions": []map[string]any{{"obj_type": "PROG", "obj_name": "ZDEMO_REPORT", "action": "add", "decision": "Y"}},
			}},
	}}
	old := gitPollInterval
	gitPollInterval = time.Millisecond
	defer func() { gitPollInterval = old }()
	cl := NewClient("http://sap.invalid", "TESTUSER", "pw")
	st, err := cl.WaitGitImport(context.Background(), ws, "12345678")
	if err != nil {
		t.Fatal(err)
	}
	r := st.Result
	if st.State != GitJobDone || r == nil || r.Outcome != GitImportedWithErrors || r.RepoKey != "000000000007" || !r.RepoCreated || !r.PackageCreated {
		t.Fatalf("status %+v result %+v", st, r)
	}
	if len(r.Log) != 1 || r.Log[0].Type != "E" || r.Log[0].ObjName != "ZDEMO_REPORT" {
		t.Errorf("log %+v", r.Log)
	}
	if len(r.Tadir) != 1 || !r.Tadir[0].Created || r.Tadir[0].DevClass != "$ZDEMO" {
		t.Errorf("tadir %+v", r.Tadir)
	}
	if n := len(ws.actions()); n != 2 {
		t.Errorf("%d status calls, want 2", n)
	}

	// "done" without a result is not believed.
	ws = &fakeGitWS{status: []map[string]any{{"outcome": "done", "job_found": true}}}
	st, err = cl.GitImportStatus(context.Background(), ws, "12345678")
	if err != nil || st.State != GitJobUnknown {
		t.Errorf("done without a result: %+v %v", st, err)
	}
	if _, err := cl.GitImportStatus(context.Background(), ws, "1234; DROP"); err == nil {
		t.Error("a job number that is not one accepted")
	}
}

func TestGitCallNamesAMissingGitService(t *testing.T) {
	ws := &fakeTransportWS{} // answers UNKNOWN_DOMAIN for "git"
	cl := NewClient("http://sap.invalid", "TESTUSER", "pw")
	_, err := cl.StartGitImport(context.Background(), ws, demoZip(t, "PREFIX"), GitImportOptions{Package: "$ZDEMO"})
	if err == nil || !strings.Contains(err.Error(), "abapGit is not installed") {
		t.Errorf("got %v", err)
	}
}

// --- delete -------------------------------------------------------------------

func TestParseGitDeleteItems(t *testing.T) {
	items, err := ParseGitDeleteItems([]any{"prog zdemo_report", "R3TR CLAS ZCL_DEMO", map[string]any{"type": "intf", "name": "zif_demo"}, "PROG ZDEMO_REPORT"})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 || items[0] != (GitDeleteItem{"PROG", "ZDEMO_REPORT"}) || items[1] != (GitDeleteItem{"CLAS", "ZCL_DEMO"}) || items[2] != (GitDeleteItem{"INTF", "ZIF_DEMO"}) {
		t.Errorf("items %+v", items)
	}
	if items, err := ParseGitDeleteItems("PROG A, PROG B"); err != nil || len(items) != 2 {
		t.Errorf("comma list: %v %v", items, err)
	}
	for _, bad := range []any{nil, "", []any{"ZDEMO"}, []any{"PROGRAM ZDEMO"}, []any{42}, []any{"PROG A B C D"}} {
		if _, err := ParseGitDeleteItems(bad); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}

func TestCheckGitDelete(t *testing.T) {
	cases := []struct {
		name    string
		opts    []Option
		pkg, tr string
	}{
		{"read-only", []Option{WithReadOnly()}, "$ZDEMO", ""},
		{"delete disallowed", []Option{WithSafety(SafetyConfig{DisallowedOps: "D"})}, "$ZDEMO", ""},
		{"outside whitelist", []Option{WithAllowedPackages("$ZOTHER")}, "$ZDEMO", ""},
		{"transportable without transport", []Option{WithAllowTransportableEdits()}, "ZDEMO", ""},
		{"transportable without opt-in", nil, "ZDEMO", "TRXK900001"},
		{"local with transport", nil, "$ZDEMO", "TRXK900001"},
	}
	for _, c := range cases {
		cl := NewClient("http://sap.invalid", "TESTUSER", "pw", c.opts...)
		if err := cl.CheckGitDelete(c.pkg, c.tr); err == nil {
			t.Errorf("%s: accepted", c.name)
		}
	}
	if err := NewClient("http://sap.invalid", "TESTUSER", "pw", WithAllowedPackages("$ZDEMO")).CheckGitDelete("$zdemo", ""); err != nil {
		t.Errorf("allowed delete refused: %v", err)
	}
}

func pkgContents(pkg string, objs [][2]string, subs []string, repo bool) map[string]any {
	var o []map[string]any
	for _, x := range objs {
		dev := pkg
		if strings.HasPrefix(x[1], "@") { // in another package
			dev, x[1] = "$ZOTHER", x[1][1:]
		}
		o = append(o, map[string]any{"pgmid": "R3TR", "object": x[0], "obj_name": x[1], "devclass": dev})
	}
	m := map[string]any{"package": pkg, "exists": true, "objects": o, "subpackages": subs}
	if repo {
		m["repo"] = map[string]any{"key": "000000000001", "name": "demo", "offline": true}
	}
	return m
}

// gitDeleteRoute answers the ADT side of deletes: the search the gate uses
// (every name in package pkgOf[name]), LOCK, DELETE.
func gitDeleteRoute(pkgOf map[string]string, uris map[string]string, failDelete map[string]bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "informationsystem/search"):
			q := strings.ToUpper(strings.Trim(r.URL.Query().Get("query"), "*"))
			var b strings.Builder
			b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><adtcore:objectReferences xmlns:adtcore="http://www.sap.com/adt/core">`)
			if p, ok := pkgOf[q]; ok {
				fmt.Fprintf(&b, `<adtcore:objectReference adtcore:uri="%s" adtcore:type="PROG/P" adtcore:name="%s" adtcore:packageName="%s"/>`, uris[q], q, p)
			}
			b.WriteString(`</adtcore:objectReferences>`)
			_, _ = io.WriteString(w, b.String())
		case r.Method == http.MethodPost && r.URL.Query().Get("_action") == "LOCK":
			w.Header().Set("Content-Type", "application/vnd.sap.as+xml")
			_, _ = io.WriteString(w, testLockXML)
		case r.Method == http.MethodDelete:
			for n, fail := range failDelete {
				if fail && strings.EqualFold(r.URL.Path, uris[n]) {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = io.WriteString(w, "in use")
					return
				}
			}
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}
}

func deletedPaths(calls []wireCall) []string {
	var out []string
	for _, c := range calls {
		if c.method == http.MethodDelete {
			out = append(out, c.path)
		}
	}
	return out
}

func TestDeleteGitObjectsScope(t *testing.T) {
	uris := map[string]string{
		"ZDEMO_REPORT": "/sap/bc/adt/programs/programs/zdemo_report",
		"ZDEMO_KEEP":   "/sap/bc/adt/programs/programs/zdemo_keep",
		"ZDEMO_ELSE":   "/sap/bc/adt/programs/programs/zdemo_else",
		"$ZDEMO":       "/sap/bc/adt/packages/%24zdemo",
	}
	pkgOf := map[string]string{"ZDEMO_REPORT": "$ZDEMO", "ZDEMO_KEEP": "$ZDEMO", "ZDEMO_ELSE": "$ZOTHER", "$ZDEMO": "$ZDEMO"}
	rec := &adtRecorder{}
	cl := newStubbedClient(t, rec, gitDeleteRoute(pkgOf, uris, nil), WithAllowedPackages("$ZDEMO"))
	ws := &fakeGitWS{contents: []map[string]any{
		pkgContents("$ZDEMO", [][2]string{{"PROG", "ZDEMO_REPORT"}, {"PROG", "ZDEMO_KEEP"}}, nil, true),
		pkgContents("$ZDEMO", [][2]string{{"PROG", "ZDEMO_KEEP"}}, nil, false),
	}}
	res, err := cl.DeleteGitObjects(context.Background(), ws, "$ZDEMO", []GitDeleteItem{
		{"PROG", "ZDEMO_REPORT"}, {"PROG", "ZDEMO_ELSE"}, {"DEVC", "$ZDEMO"}, {"PROG", "ZDEMO_ABSENT"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, o := range res.Objects {
		got[o.Type+" "+o.Name] = o.Status
	}
	want := map[string]string{"PROG ZDEMO_REPORT": "deleted", "PROG ZDEMO_ELSE": "skipped", "DEVC $ZDEMO": "skipped", "PROG ZDEMO_ABSENT": "skipped"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: %s, want %s", k, got[k], v)
		}
	}
	if d := deletedPaths(rec.snapshot()); len(d) != 1 || d[0] != uris["ZDEMO_REPORT"] {
		t.Errorf("DELETEs %v; want exactly %s", d, uris["ZDEMO_REPORT"])
	}
	if !res.RepoDeleted || res.PackageDeleted || len(res.Remaining) != 1 {
		t.Errorf("repo deleted %t, package deleted %t, remaining %v: a package with an object left must stay", res.RepoDeleted, res.PackageDeleted, res.Remaining)
	}
	if p := ws.params("delete_repo"); p == nil || p["package"] != "$ZDEMO" || p["key"] != "000000000001" {
		t.Errorf("delete_repo %v", p)
	}
}

func TestDeleteGitObjectsRemovesTheEmptyPackageLast(t *testing.T) {
	uris := map[string]string{
		"ZDEMO_REPORT": "/sap/bc/adt/programs/programs/zdemo_report",
		"$ZDEMO":       "/sap/bc/adt/packages/%24ZDEMO",
	}
	pkgOf := map[string]string{"ZDEMO_REPORT": "$ZDEMO", "$ZDEMO": "$ZDEMO"}
	rec := &adtRecorder{}
	cl := newStubbedClient(t, rec, gitDeleteRoute(pkgOf, uris, nil), WithAllowedPackages("$ZDEMO"))
	ws := &fakeGitWS{contents: []map[string]any{
		pkgContents("$ZDEMO", [][2]string{{"PROG", "ZDEMO_REPORT"}}, nil, true),
		pkgContents("$ZDEMO", nil, nil, false),
	}}
	res, err := cl.DeleteGitObjects(context.Background(), ws, "$ZDEMO", []GitDeleteItem{{"PROG", "ZDEMO_REPORT"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	d := deletedPaths(rec.snapshot())
	if len(d) != 2 || d[0] != uris["ZDEMO_REPORT"] || d[1] != "/sap/bc/adt/packages/$ZDEMO" || !res.PackageDeleted {
		t.Errorf("DELETEs %v, package deleted %t", d, res.PackageDeleted)
	}
	if acts := strings.Join(ws.actions(), ","); acts != "package_objects,delete_repo,package_objects" {
		t.Errorf("git actions %s", acts)
	}

	// A subpackage keeps the package.
	rec = &adtRecorder{}
	cl = newStubbedClient(t, rec, gitDeleteRoute(pkgOf, uris, nil), WithAllowedPackages("$ZDEMO"))
	ws = &fakeGitWS{contents: []map[string]any{
		pkgContents("$ZDEMO", [][2]string{{"PROG", "ZDEMO_REPORT"}}, nil, true),
		pkgContents("$ZDEMO", nil, []string{"$ZDEMO_SUB"}, false),
	}}
	res, err = cl.DeleteGitObjects(context.Background(), ws, "$ZDEMO", []GitDeleteItem{{"PROG", "ZDEMO_REPORT"}}, "")
	if err != nil || res.PackageDeleted || len(deletedPaths(rec.snapshot())) != 1 {
		t.Errorf("a package with a subpackage was deleted: %+v %v", res, err)
	}
}

// When an object cannot be deleted, the repository and the package stay.
func TestDeleteGitObjectsStopsOnFailure(t *testing.T) {
	uris := map[string]string{"ZDEMO_REPORT": "/sap/bc/adt/programs/programs/zdemo_report", "$ZDEMO": "/sap/bc/adt/packages/%24ZDEMO"}
	pkgOf := map[string]string{"ZDEMO_REPORT": "$ZDEMO", "$ZDEMO": "$ZDEMO"}
	rec := &adtRecorder{}
	cl := newStubbedClient(t, rec, gitDeleteRoute(pkgOf, uris, map[string]bool{"ZDEMO_REPORT": true}), WithAllowedPackages("$ZDEMO"))
	ws := &fakeGitWS{contents: []map[string]any{pkgContents("$ZDEMO", [][2]string{{"PROG", "ZDEMO_REPORT"}}, nil, true)}}
	res, err := cl.DeleteGitObjects(context.Background(), ws, "$ZDEMO", []GitDeleteItem{{"PROG", "ZDEMO_REPORT"}}, "")
	if err == nil || res == nil || res.RepoDeleted || res.PackageDeleted {
		t.Fatalf("a failed delete went on: %+v %v", res, err)
	}
	if acts := strings.Join(ws.actions(), ","); acts != "package_objects" {
		t.Errorf("git actions after the failure: %s", acts)
	}
}

// The whitelist and read-only refuse before anything is read or sent; an
// object the package's TADIR has in another package is never deleted, even
// when the gate would allow that package.
func TestDeleteGitObjectsGatesAndForeignObjects(t *testing.T) {
	ws := &fakeGitWS{}
	cl := NewClient("http://sap.invalid", "TESTUSER", "pw", WithReadOnly())
	if _, err := cl.DeleteGitObjects(context.Background(), ws, "$ZDEMO", []GitDeleteItem{{"PROG", "ZDEMO_REPORT"}}, ""); err == nil || len(ws.actions()) != 0 {
		t.Errorf("read-only: %v, %v", err, ws.actions())
	}

	uris := map[string]string{"ZDEMO_ELSE": "/sap/bc/adt/programs/programs/zdemo_else"}
	rec := &adtRecorder{}
	cl = newStubbedClient(t, rec, gitDeleteRoute(map[string]string{"ZDEMO_ELSE": "$ZOTHER"}, uris, nil)) // no whitelist at all
	ws = &fakeGitWS{contents: []map[string]any{pkgContents("$ZDEMO", [][2]string{{"PROG", "@ZDEMO_ELSE"}}, nil, false)}}
	res, _ := cl.DeleteGitObjects(context.Background(), ws, "$ZDEMO", []GitDeleteItem{{"PROG", "ZDEMO_ELSE"}}, "")
	if d := deletedPaths(rec.snapshot()); len(d) != 0 {
		t.Errorf("an object of another package was deleted: %v (%+v)", d, res)
	}
}

func TestGitObjectURL(t *testing.T) {
	for typ, want := range map[string]string{
		"PROG": "/sap/bc/adt/programs/programs/zdemo",
		"CLAS": "/sap/bc/adt/oo/classes/zdemo",
		"DEVC": "/sap/bc/adt/packages/ZDEMO",
	} {
		if got, ok := GitObjectURL(typ, "ZDEMO"); !ok || got != want {
			t.Errorf("%s: %q", typ, got)
		}
	}
	if u, _ := GitObjectURL("CLAS", "/DEMO/CL_X"); u != "/sap/bc/adt/oo/classes/%2Fdemo%2Fcl_x" {
		t.Errorf("namespaced: %s", u)
	}
	if _, ok := GitObjectURL("SUSC", "ZDEMO"); ok {
		t.Error("a type with no ADT delete here claimed one")
	}
}
