package mcp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// --- create TABL: client field and field attributes (issue #254) ---

type tableStub struct {
	mu      sync.Mutex
	calls   int
	sources []string
}

func newCreateTableTestServer(t *testing.T) (*Server, *tableStub) {
	t.Helper()
	stub := &tableStub{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		stub.mu.Lock()
		stub.calls++
		if r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/source/main") {
			stub.sources = append(stub.sources, string(body))
		}
		stub.mu.Unlock()

		w.Header().Set("X-CSRF-Token", "TOKEN")
		switch {
		case r.Method == http.MethodPost && r.URL.Query().Get("_action") == "LOCK":
			w.Header().Set("Content-Type", "application/vnd.sap.as+xml")
			_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?>
<asx:abap xmlns:asx="http://www.sap.com/abapxml" version="1.0"><asx:values><DATA>
<LOCK_HANDLE>HANDLE-1</LOCK_HANDLE><IS_LOCAL>X</IS_LOCAL>
</DATA></asx:values></asx:abap>`)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(ts.Close)

	server := NewServer(&Config{
		BaseURL:            ts.URL,
		Username:           "u",
		Password:           "p",
		Client:             "001",
		Language:           "EN",
		InsecureSkipVerify: true,
	})
	if server == nil {
		t.Fatal("NewServer returned nil")
	}
	return server, stub
}

func (s *tableStub) snapshot() (int, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls, append([]string(nil), s.sources...)
}

func createTable(t *testing.T, server *Server, params map[string]any) (string, bool) {
	t.Helper()
	res, err := server.handleCreateTable(context.Background(), newRequest(params))
	if err != nil {
		t.Fatalf("handleCreateTable: %v", err)
	}
	return uploadResultText(res), res.IsError
}

func TestCreateTable_MANDTFieldIsNotDoubled(t *testing.T) {
	server, stub := newCreateTableTestServer(t)
	text, isErr := createTable(t, server, map[string]any{
		"name":        "ZGOGEN_T_DBW",
		"description": "demo",
		"package":     "$TMP",
		"fields":      `[{"name":"MANDT","type":"MANDT","key":true,"notNull":true},{"name":"ID","type":"CHAR10","key":true},{"name":"VAL","type":"INT4"}]`,
	})
	if isErr {
		t.Fatalf("create refused: %s", text)
	}
	_, sources := stub.snapshot()
	if len(sources) != 1 {
		t.Fatalf("want one source PUT, got %d", len(sources))
	}
	if strings.Contains(sources[0], "key client") {
		t.Errorf("a second client key field was added in front of MANDT:\n%s", sources[0])
	}
	if !strings.Contains(sources[0], "key mandt : mandt not null;") {
		t.Errorf("MANDT is not the client key field:\n%s", sources[0])
	}
	if !strings.Contains(text, `"client_field": "MANDT"`) {
		t.Errorf("result does not name the client field: %s", text)
	}
}

func TestCreateTable_ClientIndependent(t *testing.T) {
	server, stub := newCreateTableTestServer(t)
	text, isErr := createTable(t, server, map[string]any{
		"name":             "ZDEMO_NOCLNT",
		"description":      "demo",
		"fields":           `[{"name":"ID","type":"CHAR10","key":true}]`,
		"client_dependent": false,
	})
	if isErr {
		t.Fatalf("create refused: %s", text)
	}
	_, sources := stub.snapshot()
	if len(sources) != 1 || strings.Contains(sources[0], "abap.clnt") {
		t.Fatalf("client-independent table got a client field:\n%v", sources)
	}
	if !strings.Contains(text, `"client_dependent": false`) {
		t.Errorf("result does not say the table is client-independent: %s", text)
	}
}

func TestCreateTable_DefaultStillAddsClient(t *testing.T) {
	server, stub := newCreateTableTestServer(t)
	text, isErr := createTable(t, server, map[string]any{
		"name":        "ZDEMO_DEFAULT",
		"description": "demo",
		"fields":      `[{"name":"ID","type":"CHAR10","key":true}]`,
	})
	if isErr {
		t.Fatalf("create refused: %s", text)
	}
	_, sources := stub.snapshot()
	if len(sources) != 1 || !strings.Contains(sources[0], "key client : abap.clnt not null;") {
		t.Fatalf("default create lost its client field:\n%v", sources)
	}
	if !strings.Contains(text, `"client_field_added": true`) {
		t.Errorf("result does not say a client field was added: %s", text)
	}
}

func TestCreateTable_RefusedBeforeSAP(t *testing.T) {
	cases := map[string]map[string]any{
		"misspelt not_null": {
			"fields": `[{"name":"ID","type":"CHAR10","key":true},{"name":"VAL","type":"INT4","not_null":true}]`,
		},
		"client_dependent not a bool": {
			"fields":           `[{"name":"ID","type":"CHAR10","key":true}]`,
			"client_dependent": "maybe",
		},
		"MANDT not first": {
			"fields": `[{"name":"ID","type":"CHAR10","key":true},{"name":"MANDT","type":"MANDT","key":true}]`,
		},
	}
	for name, params := range cases {
		t.Run(name, func(t *testing.T) {
			server, stub := newCreateTableTestServer(t)
			params["name"] = "ZDEMO_BAD"
			params["description"] = "demo"
			text, isErr := createTable(t, server, params)
			if !isErr {
				t.Fatalf("accepted: %s", text)
			}
			if calls, _ := stub.snapshot(); calls != 0 {
				t.Errorf("a refused create still made %d request(s) to SAP", calls)
			}
		})
	}
}
