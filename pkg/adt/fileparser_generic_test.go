package adt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// parseWithin runs ParseABAPFile on a goroutine and fails the test if it does
// not return in time. Before issue #237 was fixed, a generic .abap file sent
// ParseABAPFile and parseFromContent into endless mutual recursion; the stack
// grew until the runtime killed the whole process. The guard turns a
// regression back into that state into a quick, named failure (a stack
// overflow still ends the test binary, but a hang no longer can).
func parseWithin(t *testing.T, path string) (*ABAPFileInfo, error) {
	t.Helper()
	type outcome struct {
		info *ABAPFileInfo
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		info, err := ParseABAPFile(path)
		done <- outcome{info, err}
	}()
	select {
	case o := <-done:
		return o.info, o.err
	case <-time.After(5 * time.Second):
		t.Fatalf("ParseABAPFile(%s) did not return within 5s", filepath.Base(path))
		return nil, nil
	}
}

func writeFixture(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing the fixture: %v", err)
	}
	return path
}

// Every generic .abap file whose content named a type recursed before the
// fix. Each of these must now come back with that type and its name.
func TestAGenericABAPFileIsTypedFromItsContent(t *testing.T) {
	cases := []struct {
		file, content string
		wantType      CreatableObjectType
		wantName      string
	}{
		{"zdemo.abap", "*& Report ZDEMO\nREPORT zdemo.\nWRITE 'hi'.\n", ObjectTypeProgram, "ZDEMO"},
		{"zdemo2.abap", "PROGRAM zdemo2 MESSAGE-ID zz.\n", ObjectTypeProgram, "ZDEMO2"},
		{"zcl_demo.abap", "CLASS zcl_demo DEFINITION\n  PUBLIC\n  FINAL\n  CREATE PUBLIC.\nENDCLASS.\nCLASS zcl_demo IMPLEMENTATION.\nENDCLASS.\n", ObjectTypeClass, "ZCL_DEMO"},
		{"zif_demo.abap", "\"! An interface\nINTERFACE zif_demo PUBLIC.\nENDINTERFACE.\n", ObjectTypeInterface, "ZIF_DEMO"},
		{"zdemo_fg.abap", "FUNCTION-POOL zdemo_fg.\n", ObjectTypeFunctionGroup, "ZDEMO_FG"},
		{"z_demo_func.abap", "FUNCTION z_demo_func.\nENDFUNCTION.\n", ObjectTypeFunctionMod, "Z_DEMO_FUNC"},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			info, err := parseWithin(t, writeFixture(t, tc.file, tc.content))
			if err != nil {
				t.Fatalf("parsing: %v", err)
			}
			if info.ObjectType != tc.wantType || info.ObjectName != tc.wantName {
				t.Errorf("got %s %s, want %s %s", info.ObjectType, info.ObjectName, tc.wantType, tc.wantName)
			}
		})
	}
}

// The exact file from issue #237: abapGit's own name for a function module.
func TestAnAbapGitFunctionModuleFileCarriesItsGroup(t *testing.T) {
	path := writeFixture(t, "zrepro.fugr.z_repro_func.abap", "FUNCTION z_repro_func.\nENDFUNCTION.\n")
	info, err := parseWithin(t, path)
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	if info.ObjectType != ObjectTypeFunctionMod || info.ObjectName != "Z_REPRO_FUNC" || info.ParentName != "ZREPRO" {
		t.Errorf("got %s %s in group %q, want FUGR/FF Z_REPRO_FUNC in ZREPRO", info.ObjectType, info.ObjectName, info.ParentName)
	}

	ns := writeFixture(t, "#aif#util.fugr.#aif#func.abap", "FUNCTION /aif/func.\nENDFUNCTION.\n")
	info, err = parseWithin(t, ns)
	if err != nil {
		t.Fatalf("parsing the namespaced module: %v", err)
	}
	if info.ObjectName != "/AIF/FUNC" || info.ParentName != "/AIF/UTIL" {
		t.Errorf("namespaced: got %s in %q", info.ObjectName, info.ParentName)
	}
}

// The group's own includes share the {group}.fugr.{x}.abap pattern. They are
// not modules, and saying so beats deploying them as something else.
func TestAFunctionGroupIncludeIsRefusedClearly(t *testing.T) {
	path := writeFixture(t, "zrepro.fugr.lzreprotop.abap", "FUNCTION-POOL zrepro.\nDATA gv_x TYPE i.\n")
	_, err := parseWithin(t, path)
	if err == nil || !strings.Contains(err.Error(), "function group ZREPRO") {
		t.Fatalf("expected a refusal naming the group, got %v", err)
	}
}

// A file opening with a local class could be a global class written without
// PUBLIC or a program include; neither guess is safe, so it is an error that
// says how to name the file.
func TestALocalClassInAGenericFileIsAnError(t *testing.T) {
	path := writeFixture(t, "zdemo_cls.abap", "CLASS lcl_helper DEFINITION.\nENDCLASS.\n")
	_, err := parseWithin(t, path)
	if err == nil || !strings.Contains(err.Error(), ".incl.abap") {
		t.Fatalf("expected an error pointing at the suffixes, got %v", err)
	}
}

// Before issue #235 an include was exported as {name}.abap. Such a file holds
// no statement naming its type, and it must still read back as the include.
func TestAnOldIncludeExportStillReadsBack(t *testing.T) {
	path := writeFixture(t, "zdemo_top.abap", "*&---------------------------------------------------------------------*\n*& Include ZDEMO_TOP\nDATA gv_count TYPE i.\n")
	info, err := parseWithin(t, path)
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	if info.ObjectType != ObjectTypeInclude || info.ObjectName != "ZDEMO_TOP" {
		t.Errorf("got %s %s, want PROG/I ZDEMO_TOP", info.ObjectType, info.ObjectName)
	}
}

func TestAGenericFileThatIsNothingIsAnError(t *testing.T) {
	for name, content := range map[string]string{
		"empty.abap":          "",
		"comments.abap":       "* only a comment\n\" and another\n",
		"zfoo.something.abap": "DATA x TYPE i.\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseWithin(t, writeFixture(t, name, content)); err == nil {
				t.Error("expected an error, got a type")
			}
		})
	}
}

// Export then import: every name ExportToFile writes must parse back as the
// type and name it was written for.
func TestExportedFileNamesParseBackAsTheSameObject(t *testing.T) {
	cases := []struct {
		objType       CreatableObjectType
		name, parent  string
		content       string
		wantFile      string
		wantParentOut string
	}{
		{ObjectTypeInclude, "ZDEMO_TOP", "", "DATA gv_count TYPE i.\n", "zdemo_top.incl.abap", ""},
		{ObjectTypeInclude, "/DMO/DEMO_TOP", "", "DATA gv_count TYPE i.\n", "#dmo#demo_top.incl.abap", ""},
		{ObjectTypeProgram, "ZDEMO", "", "REPORT zdemo.\n", "zdemo.prog.abap", ""},
		{ObjectTypeClass, "ZCL_DEMO", "", "CLASS zcl_demo DEFINITION PUBLIC.\nENDCLASS.\n", "zcl_demo.clas.abap", ""},
		{ObjectTypeInterface, "ZIF_DEMO", "", "INTERFACE zif_demo PUBLIC.\nENDINTERFACE.\n", "zif_demo.intf.abap", ""},
		{ObjectTypeFunctionGroup, "ZDEMO_FG", "", "FUNCTION-POOL zdemo_fg.\n", "zdemo_fg.fugr.abap", ""},
		{ObjectTypeFunctionMod, "Z_DEMO_CALL", "ZDEMO_FG", "FUNCTION z_demo_call.\nENDFUNCTION.\n", "zdemo_fg.fugr.z_demo_call.func.abap", "ZDEMO_FG"},
	}
	for _, tc := range cases {
		t.Run(tc.wantFile, func(t *testing.T) {
			dir := t.TempDir()
			path := ExportFilePath(tc.objType, tc.name, tc.parent, dir)
			if filepath.Base(path) != tc.wantFile {
				t.Fatalf("exported as %s, want %s", filepath.Base(path), tc.wantFile)
			}
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			info, err := parseWithin(t, path)
			if err != nil {
				t.Fatalf("parsing the export: %v", err)
			}
			if info.ObjectType != tc.objType || info.ObjectName != tc.name || info.ParentName != tc.wantParentOut {
				t.Errorf("read back %s %s (parent %q), wrote %s %s (parent %q)",
					info.ObjectType, info.ObjectName, info.ParentName, tc.objType, tc.name, tc.wantParentOut)
			}
		})
	}
}

// An explicit file path is honoured; for an include that includes the plain
// .abap suffix it used to be written with.
func TestExportFilePathKeepsAnExplicitFile(t *testing.T) {
	for _, tc := range []struct {
		objType CreatableObjectType
		out     string
	}{
		{ObjectTypeInclude, "/x/zdemo_top.incl.abap"},
		{ObjectTypeInclude, "/x/zdemo_top.abap"},
		{ObjectTypeProgram, "/x/zdemo.prog.abap"},
	} {
		if got := ExportFilePath(tc.objType, "ZDEMO", "", tc.out); got != tc.out {
			t.Errorf("%s: got %s, want the path as given", tc.out, got)
		}
	}
}
