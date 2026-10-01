package embedded

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ZCL_VSP_GIT_SERVICE imports an abapGit zip into a package. These checks read
// the source vsp install deploys and fail on any statement that would let it
// do more than that: reach another system, start an arbitrary program, run
// in a task of its own, wait in the APC session, write a database table
// other than its own INDX(ZV) area, or decide past the caller's overwrite
// and package choices.

// gitServiceFunctions are the only function modules the service may call.
var gitServiceFunctions = map[string]bool{
	// The import runs as background job ZVSP_GIT_IMPORT: abapGit commits
	// with WAIT, which an APC session may not (APC_ILLEGAL_STATEMENT, probed
	// live), and an import may run for minutes.
	"JOB_OPEN":   true,
	"JOB_SUBMIT": true,
	"JOB_CLOSE":  true,
	// The job step's parameters: a protected variant per job (an APC session
	// may not SUBMIT), deleted once its job has ended.
	"RS_CREATE_VARIANT":    true,
	"RS_VARIANT_DELETE":    true,
	"GET_JOB_RUNTIME_INFO": true,
	// import_status reads the job's log (read-only).
	"BP_JOBLOG_READ": true,
	// The export's base64 (older code).
	"SSFC_BASE64_DECODE": true,
	"SSFC_BASE64_ENCODE": true,
}

func gitServiceSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "src", "zcl_vsp_git_service.clas.abap"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// dbWriteRe finds statements that write a database table.
var (
	dbWriteRe   = regexp.MustCompile(`^(INSERT|UPDATE|MODIFY|DELETE FROM)\b`)
	gitDynCall  = regexp.MustCompile(`CALL METHOD \S*(->|=>)\(`)
	gitDynFunc  = regexp.MustCompile(`(?i)^CALL FUNCTION \(`)
	assignRe    = regexp.MustCompile(`^(LV_REPORT)\s*=\s*(.+)$`)
	jobNameDecl = regexp.MustCompile(`CONSTANTS C_JOB_NAME TYPE TBTCJOB-JOBNAME VALUE '([A-Z0-9_]+)'`)
)

// checkGitService returns every rule src breaks.
func checkGitService(src string) []string {
	var bad []string
	stmts := abapStatements(src)
	upSrc := strings.ToUpper(strings.Join(stmts, "\n"))

	if m := jobNameDecl.FindStringSubmatch(upSrc); m == nil || m[1] != "ZVSP_GIT_IMPORT" {
		bad = append(bad, "c_job_name must be the constant 'ZVSP_GIT_IMPORT'")
	}

	for _, st := range stmts {
		up := strings.ToUpper(st)
		if gitDynFunc.MatchString(st) {
			bad = append(bad, "dynamic CALL FUNCTION: "+st)
			continue
		}
		if m := callFunctionRe.FindStringSubmatch(st); m != nil {
			name := m[1]
			if !strings.HasPrefix(name, "'") || !strings.HasSuffix(name, "'") {
				bad = append(bad, "dynamic CALL FUNCTION "+name+": the function called must be a literal")
				continue
			}
			name = strings.ToUpper(strings.Trim(name, "'"))
			for _, kw := range []string{" DESTINATION ", " STARTING NEW TASK", " IN BACKGROUND TASK", " IN BACKGROUND UNIT", " IN UPDATE TASK"} {
				if strings.Contains(up+" ", kw) {
					bad = append(bad, "CALL FUNCTION '"+name+"' with"+kw+": every call must run here, synchronously")
				}
			}
			if !gitServiceFunctions[name] {
				bad = append(bad, "CALL FUNCTION '"+name+"' is not one the git service may call")
			}
			switch name {
			case "JOB_SUBMIT":
				if m := bindingRe("REPORT").FindStringSubmatch(up); m == nil || m[1] != "LV_REPORT" {
					bad = append(bad, "JOB_SUBMIT: report must be lv_report (c_job_name)")
				}
				if m := bindingRe("VARIANT").FindStringSubmatch(up); m == nil || m[1] != "LV_VARIANT" {
					bad = append(bad, "JOB_SUBMIT: variant must be lv_variant, the job's own")
				}
				ms := bindingRe("AUTHCKNAM").FindAllStringSubmatch(up, -1)
				if len(ms) == 0 {
					bad = append(bad, "JOB_SUBMIT: authcknam must be sy-uname")
				}
				for _, m := range ms {
					if m[1] != "SY-UNAME" {
						bad = append(bad, "JOB_SUBMIT: authcknam must be sy-uname")
					}
				}
				for _, p := range []string{"COMMANDNAME", "EXTPGM_NAME", "EXTPGM_PARAM", "OPERATINGSYSTEM", "TARGETSYSTEM"} {
					if regexp.MustCompile(`\b` + p + `\s*=`).MatchString(up) {
						bad = append(bad, "JOB_SUBMIT: "+p+" must not be passed")
					}
				}
			case "RS_CREATE_VARIANT", "RS_VARIANT_DELETE":
				key := map[string]string{"RS_CREATE_VARIANT": "CURR_REPORT", "RS_VARIANT_DELETE": "REPORT"}[name]
				if m := bindingRe(key).FindStringSubmatch(up); m == nil || m[1] != "LV_REPORT" {
					bad = append(bad, name+": only variants of lv_report (c_job_name)")
				}
			}
			continue
		}
		// No other program is started, no screen, no transaction.
		for _, kw := range []string{"SUBMIT ", "CALL TRANSACTION", "LEAVE TO TRANSACTION", "CALL SCREEN", "CALL SELECTION-SCREEN",
			"GENERATE SUBROUTINE POOL", "INSERT REPORT", "EXEC SQL", "OPEN DATASET", "DELETE DATASET", "CALL 'SYSTEM'"} {
			if strings.HasPrefix(up, kw) || strings.Contains(up, " "+kw) {
				bad = append(bad, "statement not allowed in the git service: "+st)
			}
		}
		// Nothing waits in the APC session.
		if strings.Contains(up, "COMMIT WORK AND WAIT") || strings.HasPrefix(up, "WAIT ") || strings.Contains(up, "SET UPDATE TASK LOCAL") {
			bad = append(bad, "the service never waits (an APC session may not): "+st)
		}
		if strings.HasPrefix(up, "CREATE OBJECT") && strings.Contains(up, "TYPE (") {
			bad = append(bad, "dynamic CREATE OBJECT: "+st)
		}
		// One dynamic method call: abapGit's new_offline, whose name
		// parameter changed between releases.
		if gitDynCall.MatchString(up) && up != "CALL METHOD LI_SRV->('NEW_OFFLINE') PARAMETER-TABLE LT_PARAMS" {
			bad = append(bad, "dynamic method call other than new_offline: "+st)
		}
		// Database writes: its own INDX(ZV) area only.
		if dbWriteRe.MatchString(up) {
			switch {
			case strings.HasPrefix(up, "DELETE FROM DATABASE INDX(ZV) ID "):
			case strings.HasPrefix(up, "DELETE FROM INDX WHERE RELID = 'ZV' AND SRTFD LIKE 'VSPGITR%'"):
			default:
				bad = append(bad, "database write outside INDX(ZV): "+st)
			}
		}
		if strings.HasPrefix(up, "EXPORT ") && strings.Contains(up, " TO DATABASE ") && !strings.Contains(up, " TO DATABASE INDX(ZV) ") {
			bad = append(bad, "EXPORT TO DATABASE outside INDX(ZV): "+st)
		}
		if m := assignRe.FindStringSubmatch(up); m != nil && m[2] != "C_JOB_NAME" {
			bad = append(bad, "lv_report may only be c_job_name: "+st)
		}
	}
	bad = append(bad, checkGitPolicy(stmts)...)
	return bad
}

// checkGitPolicy pins the import's decisions to the caller's choices.
func checkGitPolicy(stmts []string) []string {
	var bad []string
	ev := methodStatements(stmts, "EVALUATE_CHECKS")
	if len(ev) == 0 {
		return []string{"evaluate_checks is missing"}
	}
	// Each WHEN branch of the action CASE: what it sets.
	branch := map[string][]string{}
	cur := ""
	for _, st := range ev {
		up := strings.ToUpper(st)
		switch {
		case strings.HasPrefix(up, "WHEN "):
			cur = up
		case up == "ENDCASE":
			cur = ""
		case cur != "":
			branch[cur] = append(branch[cur], up)
		}
	}
	joined := func(k string) string { return strings.Join(branch[k], "\n") }
	if b := joined("WHEN LC_DELETE"); !strings.Contains(b, "<LS_OVER>-DECISION = 'N'") || strings.Contains(b, "<LS_OVER>-DECISION = 'Y'") {
		bad = append(bad, "a local object the zip does not have must always be kept (decision 'N')")
	}
	if b := joined("WHEN LC_UPDATE OR LC_OVERWRITE OR LC_DELETE_ADD"); !regexp.MustCompile(`IF IS_PARAMS-OVERWRITE = ABAP_TRUE\n<LS_OVER>-DECISION = 'Y'\nELSE\n<LS_OVER>-DECISION = 'N'`).MatchString(b) {
		bad = append(bad, "an existing object may be changed only with overwrite = true")
	}
	for _, w := range []string{"WHEN LC_NO_SUPPORT", "WHEN LC_PACKMOVE", "WHEN LC_DATA_LOSS", "WHEN OTHERS"} {
		if b := joined(w); !strings.Contains(b, "EV_CODE = ") || !strings.Contains(b, "RETURN") {
			bad = append(bad, w+" must refuse the import")
		}
	}
	// Conflicts and unmet requirements refuse before any decision.
	up := strings.ToUpper(strings.Join(ev, "\n"))
	for _, cond := range []string{"IF CS_CHECKS-REQUIREMENTS-MET = 'N'", "IF CS_CHECKS-DEPENDENCIES-MET = 'N'",
		"IF CS_CHECKS-WARNING_PACKAGE IS NOT INITIAL", "IF CS_CHECKS-DATA_LOSS IS NOT INITIAL", "IF LT_REFUSED IS NOT INITIAL"} {
		// The branch, up to its RETURN, sets the refusal code.
		i := strings.Index(up, cond)
		seg := ""
		if i >= 0 {
			seg = up[i:]
			if j := strings.Index(seg, "\nRETURN"); j >= 0 {
				seg = seg[:j]
			}
		}
		if !strings.Contains(seg, "EV_CODE = ") {
			bad = append(bad, "evaluate_checks must refuse on: "+cond)
		}
	}
	// The transport is the caller's, never one found on the system.
	for _, st := range ev {
		u := strings.ToUpper(st)
		if strings.HasPrefix(u, "CS_CHECKS-TRANSPORT-TRANSPORT = ") && u != "CS_CHECKS-TRANSPORT-TRANSPORT = IS_PARAMS-TRANSPORT" {
			bad = append(bad, "the transport may only be the caller's: "+st)
		}
	}

	// do_import: package check and policy before deserialize, deserialize
	// only when nothing refused.
	imp := methodStatements(stmts, "DO_IMPORT")
	checkPkg := indexOf(imp, "LV_MESSAGE = CHECK_PACKAGES(")
	eval := indexOf(imp, "EVALUATE_CHECKS(")
	refuse := indexOf(imp, "IF LV_CODE IS NOT INITIAL")
	deser := indexOf(imp, "LI_REPO->DESERIALIZE( IS_CHECKS = LS_CHECKS")
	if checkPkg < 0 || eval < 0 || refuse < 0 || deser < 0 || !(checkPkg < eval && eval < refuse && refuse < deser) {
		bad = append(bad, "do_import must check the packages and the policy, and return on a refusal, before deserialize")
	}
	if n := strings.Count(strings.ToUpper(strings.Join(imp, "\n")), "->DESERIALIZE("); n != 1 {
		bad = append(bad, "do_import must deserialize exactly once")
	}

	// run_job: own variant, then the SHA-256 of zip and parameters, then
	// the import.
	job := methodStatements(stmts, "RUN_JOB")
	variant := indexOf(job, "IF SY-SLSET <> LV_OWN_VARIANT OR LV_VARIANT_FOUND = ABAP_FALSE OR LS_VARID-PROTECTED <> 'X' OR LS_VARID-ENAME <> SY-UNAME")
	ticket := indexOf(job, "IMPORT ZIP = LV_ZIP PARAMS = LS_PARAMS FROM DATABASE INDX(ZV)")
	sha := indexOf(job, "ELSEIF SHA_B64( SHA256( LV_ZIP ) ) <> CONDENSE( CONV STRING( IV_ZIP_SHA ) ) OR META_SHA( LS_PARAMS ) <> CONDENSE( CONV STRING( IV_META_SHA ) ) OR LS_PARAMS-PACKAGE <> LS_RESULT-PACKAGE")
	run := indexOf(job, "LS_RESULT = DO_IMPORT(")
	if variant < 0 || ticket < 0 || sha < 0 || run < 0 || !(variant < ticket && ticket < sha && sha < run) {
		bad = append(bad, "run_job must check its own variant, then the zip's and the parameters' SHA-256, before it imports")
	}
	if outside := indexOf(job, "IF SY-SUBRC <> 0 OR LV_JOBNAME <> C_JOB_NAME"); outside < 0 || outside > variant {
		bad = append(bad, "run_job must do nothing outside its own job")
	}

	// delete_repo deletes the repository of exactly the package named.
	del := methodStatements(stmts, "HANDLE_DELETE_REPO")
	if indexOf(del, "READ TABLE LT_REPOS INTO DATA(LS_REPO) WITH KEY PACKAGE = LV_PACKAGE") < 0 ||
		indexOf(del, "IF LV_KEY IS NOT INITIAL AND LV_KEY <> LS_REPO-KEY") < 0 {
		bad = append(bad, "delete_repo must delete only the repository registered for exactly that package (and key)")
	}
	if strings.Contains(strings.ToUpper(strings.Join(del, "\n")), "PURGE(") {
		bad = append(bad, "delete_repo must not purge (delete objects)")
	}
	return bad
}

func TestGitServiceOnlyImports(t *testing.T) {
	if bad := checkGitService(gitServiceSource(t)); len(bad) > 0 {
		t.Errorf("ZCL_VSP_GIT_SERVICE breaks its rules:\n  %s", strings.Join(bad, "\n  "))
	}
}

// TestGitServiceGuardBites mutates the real source the ways a change could
// widen what the service does, and requires the guard to catch each.
func TestGitServiceGuardBites(t *testing.T) {
	src := gitServiceSource(t)
	if len(checkGitService(src)) != 0 {
		t.Fatal("the unmutated source must pass first")
	}
	mutations := map[string]struct{ old, new string }{
		"job FM over RFC": {"CALL FUNCTION 'JOB_OPEN'\n      EXPORTING", "CALL FUNCTION 'JOB_OPEN' DESTINATION 'NONE'\n      EXPORTING"},
		"job in a new task": {"CALL FUNCTION 'BP_JOBLOG_READ'\n        EXPORTING",
			"CALL FUNCTION 'BP_JOBLOG_READ' STARTING NEW TASK 'T'\n        EXPORTING"},
		"another function":    {"CALL FUNCTION 'GET_JOB_RUNTIME_INFO'", "CALL FUNCTION 'RFC_ABAP_INSTALL_AND_RUN'"},
		"dynamic function":    {"CALL FUNCTION 'GET_JOB_RUNTIME_INFO'", "CALL FUNCTION ('GET_JOB_RUNTIME_INFO')"},
		"another job report":  {"    housekeeping( ).\n    lv_report = c_job_name.", "    housekeeping( ).\n    lv_report = iv_report."},
		"job as another user": {"        authcknam         = sy-uname", "        authcknam         = 'DDIC'"},
		"job runs a command": {"        authcknam         = sy-uname",
			"        authcknam         = sy-uname\n        extpgm_name       = lv_cmd"},
		"report literal changed": {"VALUE 'ZVSP_GIT_IMPORT'", "VALUE 'RSPARAM'"},
		"submit":                 {"    housekeeping( ).\n", "    housekeeping( ).\n    SUBMIT (lv_prog) AND RETURN.\n"},
		"call transaction":       {"    housekeeping( ).\n", "    housekeeping( ).\n    CALL TRANSACTION 'SE38'.\n"},
		"commit and wait in APC": {"    \" The ticket is on the database before the job can start.\n    COMMIT WORK.",
			"    \" The ticket is on the database before the job can start.\n    COMMIT WORK AND WAIT."},
		"write another table":  {"    COMMIT WORK.\n  ENDMETHOD.", "    COMMIT WORK.\n    DELETE FROM tadir WHERE devclass = @is_params-package.\n  ENDMETHOD."},
		"export elsewhere":     {"EXPORT result = lv_json TO DATABASE indx(zv)", "EXPORT result = lv_json TO DATABASE indx(zz)"},
		"another dynamic call": {"CALL METHOD li_srv->('NEW_OFFLINE') PARAMETER-TABLE lt_params.", "CALL METHOD li_srv->('PURGE') PARAMETER-TABLE lt_params."},
		"delete local objects": {"          ls_decision-action = `delete`.\n          <ls_over>-decision = 'N'.",
			"          ls_decision-action = `delete`.\n          <ls_over>-decision = 'Y'."},
		"overwrite regardless": {"          IF is_params-overwrite = abap_true.\n            <ls_over>-decision = 'Y'.",
			"          IF is_params-overwrite = abap_true OR 1 = 1.\n            <ls_over>-decision = 'Y'."},
		"package conflict tolerated": {"      ev_code = `PACKAGE_CONFLICT`.\n      ev_message = |Objects of the zip",
			"      ev_message = |Objects of the zip"},
		"packmove allowed": {"        WHEN lc_packmove.\n          ev_code = `PACKAGE_CONFLICT`.",
			"        WHEN lc_packmove.\n          <ls_over>-decision = 'Y'.\n          ls_decision-action = `packmove`."},
		"transport found on the system": {"      cs_checks-transport-transport = is_params-transport.",
			"      cs_checks-transport-transport = zcl_abapgit_factory=>get_default_transport( )->get( ).\n      cs_checks-transport-transport = is_params-transport."},
		"no package check": {"        lv_message = check_packages( ii_repo = li_repo it_files = lt_files is_params = is_params ).",
			"        CLEAR lv_message."},
		"import without SHA check": {"    ELSEIF sha_b64( sha256( lv_zip ) ) <> condense( CONV string( iv_zip_sha ) )",
			"    ELSEIF 1 = 2 AND sha_b64( sha256( lv_zip ) ) <> condense( CONV string( iv_zip_sha ) )"},
		"delete any repository": {"        READ TABLE lt_repos INTO DATA(ls_repo) WITH KEY package = lv_package.\n        IF sy-subrc <> 0.\n          rs_response = err( iv_id = is_message-id iv_code = 'REPO_NOT_FOUND'",
			"        READ TABLE lt_repos INTO DATA(ls_repo) INDEX 1.\n        IF sy-subrc <> 0.\n          rs_response = err( iv_id = is_message-id iv_code = 'REPO_NOT_FOUND'"},
	}
	for name, m := range mutations {
		if !strings.Contains(src, m.old) {
			t.Errorf("%s: the source no longer contains %q; update the mutation", name, m.old)
			continue
		}
		mutated := strings.Replace(src, m.old, m.new, 1)
		if len(checkGitService(mutated)) == 0 {
			t.Errorf("%s: the guard accepted the mutated source", name)
		}
	}
}

// The job program is a shim: its parameters, and one call of run_job.
func checkGitJobProgram(src string) []string {
	var bad []string
	stmts := abapStatements(src)
	want := []string{
		"REPORT ZVSP_GIT_IMPORT",
		"PARAMETERS: P_PKG TYPE DEVCLASS, P_SHZ TYPE C LENGTH 44 LOWER CASE, P_SHM TYPE C LENGTH 44 LOWER CASE, P_PUSH TYPE C LENGTH 60 LOWER CASE",
		"START-OF-SELECTION",
		"ZCL_VSP_GIT_SERVICE=>RUN_JOB( IV_PACKAGE = P_PKG IV_ZIP_SHA = P_SHZ IV_META_SHA = P_SHM IV_PUSH_ID = P_PUSH )",
	}
	if len(stmts) != len(want) {
		bad = append(bad, "the job program must be exactly REPORT, PARAMETERS, START-OF-SELECTION and the run_job call")
		return bad
	}
	for i, st := range stmts {
		if strings.ToUpper(st) != want[i] {
			bad = append(bad, "unexpected statement: "+st)
		}
	}
	return bad
}

func TestGitJobProgramIsAShim(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "src", "zvsp_git_import.prog.abap"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if bad := checkGitJobProgram(src); len(bad) > 0 {
		t.Errorf("ZVSP_GIT_IMPORT: %s", strings.Join(bad, "; "))
	}
	mutated := strings.Replace(src, "START-OF-SELECTION.", "START-OF-SELECTION.\n  SUBMIT zother AND RETURN.", 1)
	if len(checkGitJobProgram(mutated)) == 0 {
		t.Error("the guard accepted a job program that submits another report")
	}
}

// The APC handler names neither optional service statically, so ZADT_VSP
// activates and runs where abapGit is missing; and it binds each WebSocket to
// its own extension of ZVSP_GIT /import, going on without push if that fails.
func TestAPCHandlerGitServiceIsOptional(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "src", "zcl_vsp_apc_handler.clas.abap"))
	if err != nil {
		t.Fatal(err)
	}
	up := strings.ToUpper(string(b))
	if strings.Contains(up, "NEW ZCL_VSP_GIT_SERVICE(") || strings.Contains(up, "TYPE REF TO ZCL_VSP_GIT_SERVICE") {
		t.Error("the APC handler names ZCL_VSP_GIT_SERVICE statically; without abapGit it would not activate")
	}
	if !strings.Contains(up, "( `ZCL_VSP_GIT_SERVICE` )") || !strings.Contains(up, "CREATE OBJECT LO_SERVICE TYPE (LV_CLASS)") {
		t.Error("the APC handler does not create the git service dynamically")
	}
	start := strings.ToUpper(strings.Join(methodStatements(abapStatements(string(b)), "IF_APC_WSP_EXTENSION~ON_START"), "\n"))
	if !regexp.MustCompile(`BIND_AMC_MESSAGE_CONSUMER\( I_APPLICATION_ID = 'ZVSP_GIT' I_CHANNEL_ID = '/IMPORT' I_CHANNEL_EXTENSION_ID = CONV #\( MV_SESSION_ID \) \)\nLV_GIT_PUSH = ABAP_TRUE\nCATCH CX_ROOT( ##CATCH_ALL)?\nLV_GIT_PUSH = ABAP_FALSE\nENDTRY`).MatchString(start) {
		t.Error("on_start must bind ZVSP_GIT /import on its own extension and go on without push if that fails")
	}
}

func TestGitAMCApplicationDefinition(t *testing.T) {
	d := AMCGitApplicationDefinition
	for _, want := range []string{"<AMC_APPL>ZVSP_GIT</AMC_APPL>", "<CHANNEL_ID>/import</CHANNEL_ID>", "<MESSAGE_TYPE_ID>TEXT</MESSAGE_TYPE_ID>",
		"<OBJ_NAME>ZCL_VSP_GIT_SERVICE</OBJ_NAME><ACTIVITY>S</ACTIVITY>", "<OBJ_NAME>ZCL_VSP_APC_HANDLER</OBJ_NAME><ACTIVITY>C</ACTIVITY>"} {
		if !strings.Contains(d, want) {
			t.Errorf("definition lacks %s", want)
		}
	}
	if n := strings.Count(d, "<AMC_ADTCONTENTAUTHORITIES>"); n != 2 {
		t.Errorf("%d authorities; want exactly two", n)
	}
	src := gitServiceSource(t)
	pub := strings.ToUpper(strings.Join(methodStatements(abapStatements(src), "PUBLISH"), "\n"))
	if !strings.Contains(pub, "I_APPLICATION_ID = C_AMC_APP I_CHANNEL_ID = C_AMC_CHANNEL I_CHANNEL_EXTENSION_ID = CONV #( IV_PUSH_ID )") ||
		!strings.Contains(strings.ToUpper(src), "C_AMC_APP TYPE AMC_APPLICATION_ID VALUE 'ZVSP_GIT'") {
		t.Error("publish must send on ZVSP_GIT /import, on the importing session's extension")
	}
}

// Objects that name abapGit are deployed only where abapGit is.
func TestGitObjectsRequireAbapGit(t *testing.T) {
	for _, o := range GetObjects() {
		want := o.Name == "ZCL_VSP_GIT_SERVICE" || o.Name == "ZVSP_GIT_IMPORT"
		if o.RequiresAbapGit != want {
			t.Errorf("%s: RequiresAbapGit = %t, want %t", o.Name, o.RequiresAbapGit, want)
		}
		if strings.Contains(strings.ToUpper(o.Source), "ZCL_ABAPGIT_") && !o.RequiresAbapGit {
			t.Errorf("%s names abapGit but is deployed without it", o.Name)
		}
	}
}
