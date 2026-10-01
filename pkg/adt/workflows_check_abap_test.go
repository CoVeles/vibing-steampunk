package adt

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// --- line mapping ---

func TestCheckABAPFindingsAreLinesOfTheSnippet(t *testing.T) {
	code := "DATA ls TYPE t000.\nDATA(s) = |{ ls }|."
	source := executeWrapperSource("ZVSP_CHK_1", "RISK LEVEL HARMLESS", "lv_result", code)
	offset := payloadOffset(source)
	// The wrapper line that holds the snippet's second line must be the one
	// that reads it — otherwise the arithmetic below proves nothing.
	if got := strings.Split(source, "\n")[offset]; got != "DATA(s) = |{ ls }|." {
		t.Fatalf("wrapper line %d = %q, want the snippet's second line", offset+1, got)
	}

	uri := "/sap/bc/adt/programs/includes/zvsp_chk_1/source/main?context=%2fsap%2fbc%2fadt%2fprograms%2fprograms%2fzvsp_chk_1"
	findings := checkABAPFindings([]SyntaxCheckResult{
		{URI: uri, Line: offset + 1, Offset: 13, Severity: "E", Text: `"LS" cannot be converted to a character-like value.`},
	}, "ZVSP_CHK_1", offset, 2)

	if len(findings) != 1 {
		t.Fatalf("findings = %+v, want one", findings)
	}
	f := findings[0]
	if f.Line != 2 || f.Column != 14 || f.Severity != "error" || f.WrapperLine != 0 {
		t.Fatalf("finding = %+v, want line 2, column 14 (1-based), error", f)
	}
}

func TestCheckABAPFindingsOutsideTheSnippetKeepTheWrapperLine(t *testing.T) {
	// An IF the snippet never closed is reported at ENDMETHOD, two lines after
	// the snippet's last line. "Your line 4" for a one-line snippet would send
	// the caller to a line they never wrote.
	findings := checkABAPFindings([]SyntaxCheckResult{
		{URI: "/sap/bc/adt/programs/includes/zvsp_chk_1/source/main", Line: 24, Offset: 2, Severity: "E", Text: "Incorrect nesting"},
	}, "ZVSP_CHK_1", 18, 1)
	if f := findings[0]; f.Line != 0 || f.WrapperLine != 24 {
		t.Fatalf("finding = %+v, want snippet line 0 and wrapper line 24", f)
	}
}

func TestCheckABAPFindingsRefuseAnotherObjectsLine(t *testing.T) {
	findings := checkABAPFindings([]SyntaxCheckResult{
		{URI: "/sap/bc/adt/oo/classes/zcl_other/source/main", Line: 19, Offset: 4, Severity: "W", Text: "elsewhere"},
	}, "ZVSP_CHK_1", 18, 5)
	if f := findings[0]; f.Line != 0 || f.WrapperLine != 0 || f.Column != 0 || f.Severity != "warning" {
		t.Fatalf("finding = %+v, want no position at all and severity warning", f)
	}
}

func TestCheckSeverityCountsTheUnknownAsAnError(t *testing.T) {
	for in, want := range map[string]string{"E": "error", "W": "warning", "I": "info", "A": "error", "": "error"} {
		if got := checkSeverity(in); got != want {
			t.Errorf("checkSeverity(%q) = %q, want %q", in, got, want)
		}
	}
}

// --- result parsing ---

func TestCheckRunProcessedRefusesAnUncheckedReport(t *testing.T) {
	// What SAP answers, under a 200, for content it did not check: no
	// messages, which read naively is a clean snippet.
	notProcessed := []byte(`<?xml version="1.0" encoding="utf-8"?><chkrun:checkRunReports xmlns:chkrun="http://www.sap.com/adt/checkrun"><chkrun:checkReport chkrun:reporter="abapCheckRun" chkrun:triggeringUri="/sap/bc/adt/oo/classes/zcl_x" chkrun:status="notProcessed" chkrun:statusText="Resource CLASS ZCL_X does not exist."/></chkrun:checkRunReports>`)
	err := checkRunProcessed(notProcessed)
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("checkRunProcessed = %v, want an error carrying SAP's status text", err)
	}

	processed := []byte(`<?xml version="1.0" encoding="utf-8"?><chkrun:checkRunReports xmlns:chkrun="http://www.sap.com/adt/checkrun"><chkrun:checkReport chkrun:reporter="abapCheckRun" chkrun:status="processed" chkrun:statusText="Object ZX has been checked"/></chkrun:checkRunReports>`)
	if err := checkRunProcessed(processed); err != nil {
		t.Fatalf("checkRunProcessed(processed) = %v", err)
	}
	if err := checkRunProcessed([]byte(`<chkrun:checkRunReports xmlns:chkrun="http://www.sap.com/adt/checkrun"/>`)); err == nil {
		t.Fatal("an answer with no report at all was taken as a check")
	}
}

// --- the workflow against a fake SAP ---

const checkRunTypeError = `<?xml version="1.0" encoding="utf-8"?><chkrun:checkRunReports xmlns:chkrun="http://www.sap.com/adt/checkrun"><chkrun:checkReport chkrun:reporter="abapCheckRun" chkrun:status="processed" chkrun:statusText="checked"><chkrun:checkMessageList><chkrun:checkMessage chkrun:uri="/sap/bc/adt/programs/includes/{prog}/source/main?context=x#start={line},13" chkrun:type="E" chkrun:shortText="&quot;LS&quot; cannot be converted to a character-like value."/></chkrun:checkMessageList></chkrun:checkReport></chkrun:checkRunReports>`

type checkABAPServer struct {
	checkStatus int
	cancelOnRun context.CancelFunc

	mu      sync.Mutex
	calls   []string
	content string
}

func (s *checkABAPServer) start(t *testing.T, opts ...Option) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/discovery"):
			w.Header().Set("X-CSRF-Token", "TOKEN")
		case strings.Contains(r.URL.Path, "/repository/nodestructure"):
			// CreateObject's look for a package node: a read, and not part of
			// the sequence these tests pin down.
		case strings.Contains(r.URL.Path, "/checkruns"):
			s.record("checkrun")
			body, _ := io.ReadAll(r.Body)
			if m := regexp.MustCompile(`<chkrun:content>([^<]*)</chkrun:content>`).FindSubmatch(body); m != nil {
				decoded, _ := base64.StdEncoding.DecodeString(string(m[1]))
				s.mu.Lock()
				s.content = string(decoded)
				s.mu.Unlock()
			}
			if s.cancelOnRun != nil {
				s.cancelOnRun()
			}
			if s.checkStatus != 0 {
				w.WriteHeader(s.checkStatus)
				return
			}
			prog := strings.ToLower(regexp.MustCompile(`REPORT (\S+)\.`).FindStringSubmatch(s.content)[1])
			line := payloadOffset(s.content)
			w.Header().Set("Content-Type", "application/xml")
			resp := strings.NewReplacer("{prog}", prog, "{line}", strconv.Itoa(line)).Replace(checkRunTypeError)
			_, _ = w.Write([]byte(resp))
		case r.Method == http.MethodPost && r.URL.Query().Get("_action") == "LOCK":
			s.record("lock")
			w.Header().Set("Content-Type", "application/vnd.sap.as+xml")
			_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><asx:abap xmlns:asx="http://www.sap.com/abapxml"><asx:values><DATA><LOCK_HANDLE>HANDLE-1</LOCK_HANDLE></DATA></asx:values></asx:abap>`))
		case r.Method == http.MethodPost && r.URL.Query().Get("_action") == "UNLOCK":
			s.record("unlock")
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/programs/programs"):
			s.record("create")
		case r.Method == http.MethodDelete:
			s.record("delete")
		default:
			s.record(r.Method + " " + r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	cfg := NewConfig(srv.URL, "TESTUSER", "secret", opts...)
	return NewClientWithTransport(cfg, NewTransport(cfg))
}

func (s *checkABAPServer) record(call string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, call)
}

func (s *checkABAPServer) sequence() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.Join(s.calls, ",")
}

func TestCheckABAPChecksInATemporaryProgramAndRemovesIt(t *testing.T) {
	srv := &checkABAPServer{}
	result, err := srv.start(t).CheckABAP(context.Background(), "DATA ls TYPE t000.\nDATA(s) = |{ ls }|.")
	if err != nil {
		t.Fatalf("CheckABAP: %v", err)
	}
	// Create, check, delete — and nothing else: no source PUT, no
	// activation, no unit test run.
	if got, want := srv.sequence(), "create,checkrun,lock,delete"; got != want {
		t.Fatalf("requests = %s, want %s", got, want)
	}
	// The check saw the snippet inside the very wrapper ExecuteABAP uses.
	if want := executeWrapperSource(result.ProgramName, "RISK LEVEL HARMLESS", "lv_result", "DATA ls TYPE t000.\nDATA(s) = |{ ls }|."); srv.content != want {
		t.Fatalf("checked content differs from the execute wrapper:\n%s", srv.content)
	}
	if result.OK || !result.CleanedUp {
		t.Fatalf("result = %+v, want not OK and cleaned up", result)
	}
	if len(result.Findings) != 1 || result.Findings[0].Line != 1 || result.Findings[0].Column != 14 {
		t.Fatalf("findings = %+v, want one at snippet line 1, column 14", result.Findings)
	}
}

func TestCheckABAPRemovesTheProgramWhenTheCheckFails(t *testing.T) {
	srv := &checkABAPServer{checkStatus: http.StatusInternalServerError}
	result, err := srv.start(t).CheckABAP(context.Background(), "DATA lv TYPE i.")
	if err == nil {
		t.Fatal("a failed check run was reported as a check")
	}
	if got, want := srv.sequence(), "create,checkrun,lock,delete"; got != want {
		t.Fatalf("requests = %s, want %s: the temporary program must go even when the check fails", got, want)
	}
	if result == nil || !result.CleanedUp {
		t.Fatalf("result = %+v, want CleanedUp after a failed check", result)
	}
}

func TestCheckABAPRemovesTheProgramAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := &checkABAPServer{cancelOnRun: cancel}
	result, _ := srv.start(t).CheckABAP(ctx, "DATA lv TYPE i.")
	if ctx.Err() == nil {
		t.Fatal("test setup did not cancel the context during the check")
	}
	if !strings.HasSuffix(srv.sequence(), "lock,delete") || result == nil || !result.CleanedUp {
		t.Fatalf("requests = %s, result = %+v: cleanup must outlive the caller's cancellation", srv.sequence(), result)
	}
}

func TestCheckABAPCreatesNothingItCouldNotDelete(t *testing.T) {
	// Allowed to create but not to delete would leave the program behind on
	// every call, so the delete is gated before the create is sent.
	srv := &checkABAPServer{}
	_, err := srv.start(t, WithSafety(SafetyConfig{DisallowedOps: "D"})).CheckABAP(context.Background(), "DATA lv TYPE i.")
	if err == nil {
		t.Fatal("CheckABAP ran although deletion is not allowed")
	}
	if got := srv.sequence(); got != "" {
		t.Fatalf("requests = %s, want none", got)
	}
}
