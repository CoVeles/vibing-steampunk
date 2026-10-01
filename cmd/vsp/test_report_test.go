package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/oisee/vibing-steampunk/pkg/adt"
)

func cliUnitRun() *adt.UnitTestResult {
	return &adt.UnitTestResult{Classes: []adt.UnitTestClass{
		{Name: "LTC_CALC", ParentName: "ZCL_DEMO_CALC", TestMethods: []adt.UnitTestMethod{
			{Name: "ADDS"},
			{Name: "SUBTRACTS", Alerts: []adt.UnitTestAlert{{Kind: "failedAssertion", Severity: "critical",
				Title: "Critical Assertion Error: 'SUBTRACTS: 5 - 3 should be 2'", Details: []string{"Expected [2] Actual [8]"}}}},
		}},
		{Name: "LTC_SETUP", ParentName: "ZCL_DEMO_CALC", Alerts: []adt.UnitTestAlert{{Kind: "exception", Severity: "critical", Title: "Exception Error <CX_SY_ZERODIVIDE>"}}},
	}}
}

func TestPrintUnitTestReportOnlyFailures(t *testing.T) {
	var buf bytes.Buffer
	err := printUnitTestReport(&buf, cliUnitRun(), true, false)
	out := buf.String()
	if err == nil {
		t.Fatal("a failing run exited zero")
	}
	if strings.Contains(out, "ADDS") {
		t.Errorf("--only-failures printed a passing method:\n%s", out)
	}
	for _, want := range []string{"FAIL  SUBTRACTS", "Expected [2] Actual [8]", "LTC_SETUP (ZCL_DEMO_CALC)", "CX_SY_ZERODIVIDE", "1 passed, 1 failed, 1 class-level failure(s)"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
}

func TestPrintUnitTestReportJSONIsTheMCPObject(t *testing.T) {
	var buf bytes.Buffer
	_ = printUnitTestReport(&buf, cliUnitRun(), false, true)
	var got adt.UnitTestReport
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("--json is not JSON: %v\n%s", err, buf.String())
	}
	if got.OK || got.Counts.Failed != 1 || got.Counts.ClassFailures != 1 || got.Counts.Passed != 1 {
		t.Fatalf("report = %+v", got)
	}
}

// Test classes that ran no method are not a pass.
func TestPrintUnitTestReportNothingRanFails(t *testing.T) {
	run := &adt.UnitTestResult{Classes: []adt.UnitTestClass{{Name: "LTC_X", Alerts: []adt.UnitTestAlert{{Kind: "warning", Severity: "tolerable", Title: "No execution, risk level of test class exceeds upper limit"}}}}}
	var buf bytes.Buffer
	if err := printUnitTestReport(&buf, run, false, false); err == nil {
		t.Fatalf("a run where nothing ran exited zero:\n%s", buf.String())
	}
}

func TestPrintUnitTestReportJSONKeepsAngleBrackets(t *testing.T) {
	var buf bytes.Buffer
	_ = printUnitTestReport(&buf, cliUnitRun(), true, true)
	if !strings.Contains(buf.String(), "Exception Error <CX_SY_ZERODIVIDE>") {
		t.Fatalf("SAP's title was escaped:\n%s", buf.String())
	}
}
