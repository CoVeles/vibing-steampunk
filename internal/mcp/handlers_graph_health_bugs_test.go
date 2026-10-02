package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/oisee/vibing-steampunk/internal/fakesap"
	"github.com/oisee/vibing-steampunk/pkg/adt"
)

func callFake(t *testing.T, w fakesap.World, handler func(*Server) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error), args map[string]any) string {
	t.Helper()
	srv := fakesap.New(t, w)
	s := &Server{adtClient: adt.NewClient(srv.URL, "TESTUSER", "secret")}
	var req mcp.CallToolRequest
	req.Params.Arguments = args
	result, err := handler(s)(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	return toolResultText(t, result)
}

// The package listing names types the way ADT does, "CLAS/OC", and package
// health compared them with "CLAS". Every object was skipped, so a package full
// of classes reported no tests, no boundary verdict and no staleness.
func TestPackageHealthReadsObjectsListedWithTwoPartTypes(t *testing.T) {
	text := callFake(t, fakesap.Gold(), (*Server).health, map[string]any{"package": "$ZGOLD"})
	var got struct {
		Signals map[string]struct {
			Status  string         `json:"status"`
			Details map[string]any `json:"details"`
		} `json:"signals"`
	}
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("%v\n%s", err, text)
	}
	if n, _ := got.Signals["boundaries"].Details["scanned_objects"].(float64); n == 0 {
		t.Errorf("boundaries scanned no objects: %+v", got.Signals["boundaries"])
	}
	if s := got.Signals["tests"].Status; s == "NONE" {
		t.Errorf("tests: NONE, but $ZGOLD has a test class")
	}
	if s := got.Signals["staleness"].Status; s == "UNKNOWN" {
		t.Errorf("staleness: UNKNOWN, but $ZGOLD's classes have revisions")
	}
}

// A candidate whose source could not be read was listed "unconfirmed" — the
// word for "read, and the variable is not in it" — and no gap named it.
func TestWhereUsedConfigNamesAnUnreadableCandidate(t *testing.T) {
	text := callFake(t, fakesap.Gold(), (*Server).whereUsedConfig, map[string]any{"variable": "ZGOLD_VAR"})
	var got struct {
		Unsearched []adt.Unsearched `json:"unsearched"`
		Notes      []string         `json:"notes"`
	}
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("%v\n%s", err, text)
	}
	found := false
	for _, u := range got.Unsearched {
		if u.Object == "PROG ZGOLD_MISSING" && strings.Contains(u.Reason, "404") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no gap names PROG ZGOLD_MISSING with its reason:\n%s", text)
	}
	if len(got.Notes) == 0 || !strings.HasPrefix(got.Notes[0], "1 of 3 objects") {
		t.Fatalf("the note should count 1 of the 3 candidates: %q", got.Notes)
	}
}

// funcGroupWorld is a package holding one function group whose include calls
// into another package, and one of whose includes cannot be read.
func funcGroupWorld() fakesap.World {
	w := fakesap.Gold()
	w.Packages = map[string]string{"$ZFG": "", "$ZOTHER": ""}
	w.Objects = []fakesap.Object{
		{Type: "FUGR", Name: "ZFG_MAIN", Package: "$ZFG", Source: "FUNCTION-POOL zfg_main.", Parts: map[string]string{
			"LZFG_MAINU01": "FUNCTION z_fg_run.\n  DATA lo TYPE REF TO zcl_foreign.\nENDFUNCTION.",
			"LZFG_MAINU02": "",
		}},
		{Type: "CLAS", Name: "ZCL_FOREIGN", Package: "$ZOTHER"},
	}
	return w
}

// GetSource answers for a function group with its metadata as JSON, which
// parses to no dependencies. The group counted as read and its cross-package
// reference vanished, so the package came out clean.
func TestABoundaryCheckReadsAFunctionGroupsCode(t *testing.T) {
	text := callFake(t, funcGroupWorld(), (*Server).checkBoundaries, map[string]any{"package": "$ZFG"})
	if !strings.Contains(text, "ZFG_MAIN → ZCL_FOREIGN") || !strings.Contains(text, "--- VIOLATIONS (1) ---") {
		t.Fatalf("the function group's call into $ZOTHER is not reported as a violation:\n%s", text)
	}
	if !strings.Contains(text, "FUGR ZFG_MAIN: /sap/bc/adt/functions/groups/zfg_main/includes/lzfg_mainu02/source/main") {
		t.Fatalf("the include that could not be read is not named as a gap:\n%s", text)
	}
}

// moduleWorld is a package holding two function modules: one whose group is in
// the package too, and one whose group is not listed here at all.
func moduleWorld() fakesap.World {
	w := funcGroupWorld()
	w.Packages = map[string]string{"$ZFG": "", "$ZOTHER": ""}
	w.Objects = append(w.Objects,
		fakesap.Object{Type: "FUNC", Name: "Z_FG_RUN", Group: "ZFG_MAIN", Package: "$ZFG",
			Source: "FUNCTION z_fg_run.\n  DATA lo TYPE REF TO zcl_foreign.\nENDFUNCTION."},
		fakesap.Object{Type: "FUNC", Name: "Z_STRAY_FM", Group: "ZELSEWHERE_FG", Package: "$ZFG",
			Source: "FUNCTION z_stray_fm.\n  DATA lo TYPE REF TO zcl_foreign.\nENDFUNCTION."},
	)
	return w
}

// A module was skipped on the grounds that its group would be read. When the
// group is not in the package, nothing read the module at all.
func TestAModuleWhoseGroupIsNotInThePackageIsReadOnItsOwn(t *testing.T) {
	srv := fakesap.New(t, moduleWorld())
	s := &Server{adtClient: adt.NewClient(srv.URL, "TESTUSER", "secret")}
	var req mcp.CallToolRequest
	req.Params.Arguments = map[string]any{"package": "$ZFG"}
	result, err := s.handleCheckBoundaries(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	text := toolResultText(t, result)
	if !strings.Contains(text, "Z_STRAY_FM → ZCL_FOREIGN") {
		t.Fatalf("the stray module's call into $ZOTHER is not reported:\n%s", text)
	}
	log := strings.ToLower(strings.Join(srv.Log(), "\n"))
	if !strings.Contains(log, "/fmodules/z_stray_fm/source/main") {
		t.Fatalf("the stray module was not read:\n%s", log)
	}
	if strings.Contains(log, "/fmodules/z_fg_run/") {
		t.Fatalf("a module whose group is read was read again on its own:\n%s", log)
	}
}

// A listing entry of a type the scan does not read used to vanish, and the
// verdict over the rest looked complete.
func TestAnUnsupportedListingEntryIsAGap(t *testing.T) {
	w := fakesap.Narrow()
	w.Objects = append(w.Objects,
		fakesap.Object{Type: "PROG/X", Name: "ZNARROW_ODD", Package: "$ZNARROW"},
		fakesap.Object{Type: "TABL", Name: "ZNARROW_T", Package: "$ZNARROW"},
	)
	text := callFake(t, w, (*Server).checkBoundaries, map[string]any{"package": "$ZNARROW"})
	if !strings.Contains(text, "PROG/X ZNARROW_ODD: listing type PROG/X is not one this scan reads source for") {
		t.Fatalf("the unsupported entry is not named as a gap:\n%s", text)
	}
	if strings.Contains(text, "ZNARROW_T") {
		t.Fatalf("a table carries no source and should be excused, not reported:\n%s", text)
	}
}

// Health had its own scan, which took classes, programs and interfaces only:
// a package of function groups had nothing read and no verdict.
func TestPackageHealthReadsFunctionGroups(t *testing.T) {
	text := callFake(t, funcGroupWorld(), (*Server).health, map[string]any{"package": "$ZFG"})
	var got struct {
		Signals map[string]struct {
			Status string `json:"status"`
		} `json:"signals"`
	}
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("%v\n%s", err, text)
	}
	if s := got.Signals["boundaries"].Status; s != "VIOLATIONS" {
		t.Fatalf("boundaries: %s, want VIOLATIONS from the group's call into $ZOTHER\n%s", s, text)
	}
}
