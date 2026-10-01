package adt

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type includeWireCall struct {
	method string
	path   string // escaped, exactly as sent
	action string
	body   string
}

// newIncludeWriteServer answers like ADT for a class whose include writes
// are recorded. missingTestInclude makes the first PUT to includes/testclasses
// answer 404 until the include is created.
func newIncludeWriteServer(t *testing.T, missingTestInclude bool) (*Client, func() []includeWireCall) {
	return newIncludeWriteServerAnswering(t, missingTestInclude, http.StatusNotFound, "")
}

// missingIncludeED170 is what a 7.58 answers a PUT to the testclasses include
// of a class that has none: a 500, not a 404.
const missingIncludeED170 = `<?xml version="1.0" encoding="utf-8"?><exc:exception xmlns:exc="http://www.sap.com/abapxml/types/communicationframework"><namespace id="com.sap.adt"/><type id="ExceptionResourceSaveFailure"/><message lang="EN">ZCL_PROBE======================CCAU does not have any inactive version</message><properties><entry key="T100KEY-ID">ED</entry><entry key="T100KEY-NO">170</entry></properties></exc:exception>`

func newIncludeWriteServerAnswering(t *testing.T, missingTestInclude bool, missingStatus int, missingBody string) (*Client, func() []includeWireCall) {
	t.Helper()
	var mu sync.Mutex
	var calls []includeWireCall
	testIncludeExists := !missingTestInclude
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		calls = append(calls, includeWireCall{
			method: r.Method,
			path:   r.URL.EscapedPath(),
			action: r.URL.Query().Get("_action"),
			body:   string(body),
		})
		mu.Unlock()
		w.Header().Set("X-CSRF-Token", "TOKEN")
		switch {
		case r.URL.Query().Get("_action") == "LOCK":
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?>
<asx:abap xmlns:asx="http://www.sap.com/abapxml" version="1.0"><asx:values><DATA>
<LOCK_HANDLE>HANDLE-1</LOCK_HANDLE><IS_LOCAL>X</IS_LOCAL>
</DATA></asx:values></asx:abap>`)
		case strings.Contains(r.URL.Path, "/checkruns"):
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><chkrun:checkRunReports xmlns:chkrun="http://www.sap.com/adt/checkrun"/>`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/includes"):
			mu.Lock()
			testIncludeExists = true
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/includes/testclasses"):
			mu.Lock()
			exists := testIncludeExists
			mu.Unlock()
			if !exists {
				w.WriteHeader(missingStatus)
				_, _ = io.WriteString(w, missingBody)
				return
			}
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)
	snapshot := func() []includeWireCall {
		mu.Lock()
		defer mu.Unlock()
		return append([]includeWireCall(nil), calls...)
	}
	return NewClient(srv.URL, "TESTUSER", "pw"), snapshot
}

func putPaths(calls []includeWireCall) []string {
	var paths []string
	for _, c := range calls {
		if c.method == http.MethodPut {
			paths = append(paths, c.path)
		}
	}
	return paths
}

// #242: WriteSource with an include must PUT to that include and never to
// the main source.
func TestWriteSourceClassIncludeWritesIncludeNotMain(t *testing.T) {
	for _, include := range []string{"testclasses", "definitions", "implementations", "macros", "TestClasses"} {
		t.Run(include, func(t *testing.T) {
			c, snapshot := newIncludeWriteServer(t, false)
			const src = "CLASS ltcl_probe DEFINITION FOR TESTING. ENDCLASS."
			result, err := c.WriteSource(context.Background(), "CLAS", "ZCL_PROBE", src, &WriteSourceOptions{Include: include})
			if err != nil {
				t.Fatalf("WriteSource: %v", err)
			}
			if !result.Success {
				t.Fatalf("WriteSource failed: %s", result.Message)
			}
			want := "/sap/bc/adt/oo/classes/ZCL_PROBE/includes/" + strings.ToLower(include)
			puts := putPaths(snapshot())
			if len(puts) != 1 || puts[0] != want {
				t.Fatalf("PUTs = %v, want exactly [%s]", puts, want)
			}
			if result.Include != strings.ToLower(include) {
				t.Errorf("result.Include = %q", result.Include)
			}
			var locked, unlocked, activated bool
			for _, call := range snapshot() {
				switch {
				case call.action == "LOCK":
					locked = call.path == "/sap/bc/adt/oo/classes/ZCL_PROBE"
				case call.action == "UNLOCK":
					unlocked = call.path == "/sap/bc/adt/oo/classes/ZCL_PROBE"
				case strings.HasSuffix(call.path, "/activation"):
					activated = strings.Contains(call.body, `adtcore:uri="/sap/bc/adt/oo/classes/ZCL_PROBE"`)
				}
			}
			if !locked || !unlocked || !activated {
				t.Errorf("want LOCK, UNLOCK and activation on the class URL: lock=%v unlock=%v activate=%v calls=%+v", locked, unlocked, activated, snapshot())
			}
		})
	}
}

func TestWriteSourceClassIncludeMainKeepsMainPath(t *testing.T) {
	for _, include := range []string{"main", "MAIN"} {
		c, snapshot := newIncludeWriteServer(t, false)
		result, err := c.WriteSource(context.Background(), "CLAS", "ZCL_PROBE", "CLASS zcl_probe DEFINITION PUBLIC. ENDCLASS. CLASS zcl_probe IMPLEMENTATION. ENDCLASS.", &WriteSourceOptions{Include: include, Mode: WriteModeUpdate})
		if err != nil {
			t.Fatalf("WriteSource: %v", err)
		}
		if !result.Success {
			t.Fatalf("WriteSource failed: %s", result.Message)
		}
		puts := putPaths(snapshot())
		if len(puts) != 1 || puts[0] != "/sap/bc/adt/oo/classes/ZCL_PROBE/source/main" {
			t.Fatalf("include=%s PUTs = %v, want the main source", include, puts)
		}
	}
}

func TestWriteSourceClassIncludeRefusesWithoutWriting(t *testing.T) {
	tests := []struct {
		name string
		typ  string
		opts WriteSourceOptions
		want string
	}{
		{"unknown include", "CLAS", WriteSourceOptions{Include: "testclass"}, "unknown class include"},
		{"locals_def is not an ADT include", "CLAS", WriteSourceOptions{Include: "locals_def"}, "unknown class include"},
		{"not a class", "PROG", WriteSourceOptions{Include: "testclasses"}, "only valid for CLAS"},
		{"with method", "CLAS", WriteSourceOptions{Include: "testclasses", Method: "RUN"}, "method and include"},
		{"with test_source", "CLAS", WriteSourceOptions{Include: "testclasses", TestSource: "x"}, "test_source and include"},
		{"create mode", "CLAS", WriteSourceOptions{Include: "testclasses", Mode: WriteModeCreate}, "mode=create"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, snapshot := newIncludeWriteServer(t, false)
			opts := tt.opts
			result, err := c.WriteSource(context.Background(), tt.typ, "ZCL_PROBE", "CLASS ltcl DEFINITION. ENDCLASS.", &opts)
			if err != nil {
				t.Fatalf("WriteSource: %v", err)
			}
			if result.Success || !strings.Contains(result.Message, tt.want) {
				t.Fatalf("result = success=%v message=%q, want a refusal containing %q", result.Success, result.Message, tt.want)
			}
			if calls := snapshot(); len(calls) != 0 {
				t.Fatalf("a refused include write must not reach SAP; calls=%+v", calls)
			}
		})
	}
}

func TestWriteSourceClassIncludeCreatesMissingTestInclude(t *testing.T) {
	t.Run("404", func(t *testing.T) {
		testCreatesMissingTestInclude(t, http.StatusNotFound, "")
	})
	t.Run("500 ED 170, as a 7.58 answers", func(t *testing.T) {
		testCreatesMissingTestInclude(t, http.StatusInternalServerError, missingIncludeED170)
	})
}

func TestWriteSourceClassIncludeOtherFailureDoesNotCreate(t *testing.T) {
	c, snapshot := newIncludeWriteServerAnswering(t, true, http.StatusInternalServerError, "some other save failure")
	result, err := c.WriteSource(context.Background(), "CLAS", "ZCL_PROBE", "CLASS ltcl DEFINITION FOR TESTING. ENDCLASS.", &WriteSourceOptions{Include: "testclasses"})
	if err != nil {
		t.Fatalf("WriteSource: %v", err)
	}
	if result.Success {
		t.Fatalf("WriteSource succeeded on a failed PUT: %s", result.Message)
	}
	var unlocked bool
	for _, call := range snapshot() {
		if call.method == http.MethodPost && strings.HasSuffix(call.path, "/includes") {
			t.Fatalf("an unrelated failure must not create the include; calls=%+v", snapshot())
		}
		if call.action == "UNLOCK" {
			unlocked = true
		}
	}
	if !unlocked {
		t.Fatalf("a failed write must release the class lock; calls=%+v", snapshot())
	}
}

func testCreatesMissingTestInclude(t *testing.T, status int, body string) {
	c, snapshot := newIncludeWriteServerAnswering(t, true, status, body)
	result, err := c.WriteSource(context.Background(), "CLAS", "ZCL_PROBE", "CLASS ltcl DEFINITION FOR TESTING. ENDCLASS.", &WriteSourceOptions{Include: "testclasses"})
	if err != nil {
		t.Fatalf("WriteSource: %v", err)
	}
	if !result.Success || !strings.Contains(result.Message, "created") {
		t.Fatalf("result = success=%v message=%q, want the include created and written", result.Success, result.Message)
	}
	puts := putPaths(snapshot())
	if len(puts) != 2 || puts[1] != "/sap/bc/adt/oo/classes/ZCL_PROBE/includes/testclasses" {
		t.Fatalf("PUTs = %v, want the 404 attempt and the retry on the include", puts)
	}
}

// #282: a namespaced class is escaped exactly once, whether the name arrives
// raw or already escaped.
func TestWriteSourceClassIncludeNamespacedEscapedOnce(t *testing.T) {
	for _, name := range []string{"/ZDEMO/CL_PROBE", "%2FZDEMO%2FCL_PROBE"} {
		c, snapshot := newIncludeWriteServer(t, false)
		result, err := c.WriteSource(context.Background(), "CLAS", name, "CLASS ltcl DEFINITION FOR TESTING. ENDCLASS.", &WriteSourceOptions{Include: "testclasses"})
		if err != nil {
			t.Fatalf("WriteSource(%s): %v", name, err)
		}
		if !result.Success {
			t.Fatalf("WriteSource(%s) failed: %s", name, result.Message)
		}
		puts := putPaths(snapshot())
		if len(puts) != 1 || puts[0] != "/sap/bc/adt/oo/classes/%2FZDEMO%2FCL_PROBE/includes/testclasses" {
			t.Fatalf("name %s: PUTs = %v, want the include escaped once", name, puts)
		}
		for _, call := range snapshot() {
			if strings.Contains(call.path, "%25") {
				t.Fatalf("name %s: double-escaped request %s", name, call.path)
			}
		}
	}
}

func TestSplitClassIncludeURL(t *testing.T) {
	tests := []struct {
		in, class, source string
		ok, err           bool
	}{
		{"/sap/bc/adt/oo/classes/zcl_x/includes/testclasses", "/sap/bc/adt/oo/classes/zcl_x", "/sap/bc/adt/oo/classes/zcl_x/includes/testclasses", true, false},
		{"/sap/bc/adt/oo/classes/zcl_x/includes/Definitions/", "/sap/bc/adt/oo/classes/zcl_x", "/sap/bc/adt/oo/classes/zcl_x/includes/definitions", true, false},
		{"/sap/bc/adt/oo/classes/%2Fzdemo%2Fcl_x/includes/macros", "/sap/bc/adt/oo/classes/%2Fzdemo%2Fcl_x", "/sap/bc/adt/oo/classes/%2Fzdemo%2Fcl_x/includes/macros", true, false},
		{"/sap/bc/adt/oo/classes/zcl_x/includes/main", "/sap/bc/adt/oo/classes/zcl_x", "/sap/bc/adt/oo/classes/zcl_x/source/main", true, false},
		{"/sap/bc/adt/oo/classes/zcl_x/includes/localtypes", "", "", true, true},
		{"/sap/bc/adt/oo/classes/zcl_x/includes", "", "", true, true},
		{"/sap/bc/adt/oo/classes/zcl_x/includes/testclasses/source/main", "", "", true, true},
		{"/sap/bc/adt/oo/classes/zcl_x", "", "", false, false},
		{"/sap/bc/adt/oo/classes/zcl_x/source/main", "", "", false, false},
		{"/sap/bc/adt/programs/includes/zinclude", "", "", false, false},
	}
	for _, tt := range tests {
		class, source, ok, err := SplitClassIncludeURL(tt.in)
		if ok != tt.ok || (err != nil) != tt.err {
			t.Errorf("%s: ok=%v err=%v, want ok=%v err=%v", tt.in, ok, err, tt.ok, tt.err)
			continue
		}
		if !tt.err && (class != tt.class || source != tt.source) {
			t.Errorf("%s: got (%s, %s), want (%s, %s)", tt.in, class, source, tt.class, tt.source)
		}
	}
}
