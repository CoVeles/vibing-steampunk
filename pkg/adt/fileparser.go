package adt

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ABAPFileInfo contains parsed information about an ABAP source file.
type ABAPFileInfo struct {
	FilePath          string
	ObjectType        CreatableObjectType
	ObjectName        string
	ParentName        string           // For function modules: the function group name
	Description       string           // Parsed from comments if available
	ClassIncludeType  ClassIncludeType // For class includes (testclasses, definitions, etc.)
	HasDefinition     bool             // For classes
	HasImplementation bool
	HasTestClasses    bool
}

// extractFunctionGroupFromFilename extracts the function group name from abapGit-style filenames.
// Pattern: {fugr_name}.fugr.{func_name}.func.abap → FUGR_NAME
// Example: zvsp_report.fugr.z_vsp_run_report.func.abap → ZVSP_REPORT
// Example: #aif#util.fugr.#aif#func.func.abap → /AIF/UTIL (namespaced)
func extractFunctionGroupFromFilename(filePath string) string {
	baseName := filepath.Base(filePath)
	// Pattern: {fugr}.fugr.{func}.func.abap
	if strings.HasSuffix(strings.ToLower(baseName), ".func.abap") {
		// Find .fugr. in the filename
		lowerName := strings.ToLower(baseName)
		fugrIdx := strings.Index(lowerName, ".fugr.")
		if fugrIdx > 0 {
			name := strings.ToUpper(baseName[:fugrIdx])
			// Convert # back to / for namespaced objects (abapGit convention)
			name = strings.ReplaceAll(name, "#", "/")
			return name
		}
	}
	return ""
}

// extractClassNameFromFilename extracts the parent class name from abapGit-style filenames.
// Examples:
//   - zcl_foo.clas.testclasses.abap → ZCL_FOO
//   - zcl_foo.clas.locals_def.abap → ZCL_FOO
//   - zcl_foo.clas.locals_imp.abap → ZCL_FOO
//   - zcl_foo.clas.macros.abap → ZCL_FOO
//   - zcl_foo.clas.abap → ZCL_FOO
//   - #dmo#cl_flight.clas.abap → /DMO/CL_FLIGHT (namespaced)
func extractClassNameFromFilename(filePath string) string {
	baseName := filepath.Base(filePath)

	// Remove known suffixes in order of specificity
	suffixes := []string{
		".clas.testclasses.abap",
		".clas.locals_def.abap",
		".clas.locals_imp.abap",
		".clas.macros.abap",
		".clas.abap",
	}

	for _, suffix := range suffixes {
		if strings.HasSuffix(strings.ToLower(baseName), suffix) {
			name := baseName[:len(baseName)-len(suffix)]
			name = strings.ToUpper(name)
			// Convert # back to / for namespaced objects (abapGit convention)
			name = strings.ReplaceAll(name, "#", "/")
			return name
		}
	}

	return ""
}

// ParseABAPFile analyzes an ABAP source file and extracts metadata.
// It detects the object type from file extension and parses the content
// to extract the object name and other metadata.
func ParseABAPFile(filePath string) (*ABAPFileInfo, error) {
	// 1. Detect from extension
	ext := filepath.Ext(filePath)
	info := &ABAPFileInfo{FilePath: filePath}

	// Check for compound extensions
	// Note: More specific suffixes must come BEFORE less specific ones
	baseName := filepath.Base(filePath)
	switch {
	// Class includes (must be before .clas.abap)
	// For class includes, extract the class name from the filename, not content
	case strings.HasSuffix(baseName, ".clas.testclasses.abap"):
		info.ObjectType = ObjectTypeClass
		info.ClassIncludeType = ClassIncludeTestClasses
		info.ObjectName = extractClassNameFromFilename(filePath)
	case strings.HasSuffix(baseName, ".clas.locals_def.abap"):
		info.ObjectType = ObjectTypeClass
		info.ClassIncludeType = ClassIncludeDefinitions
		info.ObjectName = extractClassNameFromFilename(filePath)
	case strings.HasSuffix(baseName, ".clas.locals_imp.abap"):
		info.ObjectType = ObjectTypeClass
		info.ClassIncludeType = ClassIncludeImplementations
		info.ObjectName = extractClassNameFromFilename(filePath)
	case strings.HasSuffix(baseName, ".clas.macros.abap"):
		info.ObjectType = ObjectTypeClass
		info.ClassIncludeType = ClassIncludeMacros
		info.ObjectName = extractClassNameFromFilename(filePath)
	// Main class
	case strings.HasSuffix(baseName, ".clas.abap"):
		info.ObjectType = ObjectTypeClass
		info.ClassIncludeType = ClassIncludeMain
	case strings.HasSuffix(baseName, ".prog.abap"):
		info.ObjectType = ObjectTypeProgram
	case strings.HasSuffix(baseName, ".incl.abap"):
		info.ObjectType = ObjectTypeInclude
		info.ObjectName = strings.ToUpper(strings.ReplaceAll(strings.TrimSuffix(baseName, ".incl.abap"), "#", "/"))
	case strings.HasSuffix(baseName, ".intf.abap"):
		info.ObjectType = ObjectTypeInterface
	case strings.HasSuffix(baseName, ".fugr.abap"):
		info.ObjectType = ObjectTypeFunctionGroup
	case strings.HasSuffix(baseName, ".func.abap"):
		info.ObjectType = ObjectTypeFunctionMod
		info.ParentName = extractFunctionGroupFromFilename(filePath)
	// RAP object types (ABAPGit-compatible extensions)
	case strings.HasSuffix(baseName, ".ddls.asddls"):
		info.ObjectType = ObjectTypeDDLS
	case strings.HasSuffix(baseName, ".bdef.asbdef"):
		info.ObjectType = ObjectTypeBDEF
	case strings.HasSuffix(baseName, ".srvd.srvdsrv"):
		info.ObjectType = ObjectTypeSRVD
	// abapGit's own name for a function module: {group}.fugr.{module}.abap.
	// The group's other includes share the pattern, so the content decides.
	case fugrMemberGroup(baseName) != "":
		info.ParentName = fugrMemberGroup(baseName)
		kind, err := detectTypeFromContent(filePath)
		if err != nil {
			return nil, err
		}
		if kind != ObjectTypeFunctionMod {
			return nil, fmt.Errorf("%s is part of function group %s but holds no FUNCTION statement: a function group's own includes cannot be deployed one file at a time (a function module file starts with FUNCTION)", baseName, info.ParentName)
		}
		info.ObjectType = ObjectTypeFunctionMod
	case ext == ".abap":
		// Generic .abap: the type comes from the first statement. This used to
		// call back into ParseABAPFile on the same path, which landed here
		// again, and the recursion killed the process (issue #237).
		kind, err := detectTypeFromContent(filePath)
		if err != nil {
			return nil, err
		}
		if kind == "" {
			// No REPORT, CLASS, INTERFACE or FUNCTION statement opens it, so it
			// can only be an include. Exports before issue #235 wrote includes
			// as {name}.abap, and those still read back as the include.
			name, err := legacyIncludeName(baseName)
			if err != nil {
				return nil, err
			}
			info.ObjectType = ObjectTypeInclude
			info.ObjectName = name
		} else {
			info.ObjectType = kind
		}
	default:
		return nil, fmt.Errorf("unsupported file extension: %s (expected .clas.abap, .clas.testclasses.abap, .clas.locals_def.abap, .clas.locals_imp.abap, .prog.abap, .incl.abap, .intf.abap, .fugr.abap, .func.abap, .ddls.asddls, .bdef.asbdef, or .srvd.srvdsrv)", ext)
	}

	// 2. Parse file content to extract name and metadata
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("opening file: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	lineNum := 0
	inComment := false

	for scanner.Scan() && lineNum < 200 { // Scan first 200 lines
		line := scanner.Text()
		lineNum++

		// Parse based on object type
		switch info.ObjectType {
		case ObjectTypeClass:
			// For class includes (testclasses, locals_def, etc.), the name is already
			// extracted from the filename. Only parse from content for main class files.
			if info.ObjectName == "" {
				if name := parseClassName(line); name != "" {
					info.ObjectName = name
				}
			}
			if strings.Contains(strings.ToUpper(line), "DEFINITION") {
				info.HasDefinition = true
			}
			if strings.Contains(strings.ToUpper(line), "IMPLEMENTATION") {
				info.HasImplementation = true
			}
			if strings.Contains(strings.ToUpper(line), "FOR TESTING") {
				info.HasTestClasses = true
			}

		case ObjectTypeProgram:
			if name := parseProgramName(line); name != "" {
				info.ObjectName = name
			}

		case ObjectTypeInterface:
			if name := parseInterfaceName(line); name != "" {
				info.ObjectName = name
			}

		case ObjectTypeFunctionGroup:
			if name := parseFunctionGroupName(line); name != "" {
				info.ObjectName = name
			}

		case ObjectTypeFunctionMod:
			if name := parseFunctionModuleName(line); name != "" {
				info.ObjectName = name
			}

		// RAP object types
		case ObjectTypeDDLS:
			if name := parseDDLSName(line); name != "" {
				info.ObjectName = name
			}

		case ObjectTypeBDEF:
			if name := parseBDEFName(line); name != "" {
				info.ObjectName = name
			}

		case ObjectTypeSRVD:
			if name := parseSRVDName(line); name != "" {
				info.ObjectName = name
			}
		}

		// Parse description from header comments
		trimmed := strings.TrimSpace(line)
		if info.Description == "" {
			if strings.HasPrefix(trimmed, "*") || strings.HasPrefix(trimmed, "\"") {
				comment := strings.TrimPrefix(trimmed, "*")
				comment = strings.TrimPrefix(comment, "\"")
				comment = strings.TrimSpace(strings.TrimLeft(comment, "&"))
				comment = strings.TrimSpace(comment)

				// Skip common patterns, and the header template's own
				// "Report ZDEMO" line, which named one program "& Report ZDEMO".
				if comment != "" && !headerTitleLine.MatchString(comment) &&
					!strings.HasPrefix(comment, "-") &&
					!strings.HasPrefix(comment, "=") &&
					!strings.HasPrefix(comment, "*") &&
					!strings.Contains(strings.ToLower(comment), "author") &&
					!strings.Contains(strings.ToLower(comment), "date") &&
					len(comment) > 10 && len(comment) < 60 {
					info.Description = comment
					inComment = true
				}
			} else if inComment {
				inComment = false
			}
		}

		// Early exit if we have all required info
		if info.ObjectName != "" && info.Description != "" {
			break
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading file: %w", err)
	}

	if info.ObjectName == "" {
		return nil, fmt.Errorf("could not parse object name from file (expected CLASS/PROGRAM/INTERFACE/FUNCTION GROUP/FUNCTION statement in first 200 lines)")
	}

	// Provide default description if none found
	if info.Description == "" {
		info.Description = fmt.Sprintf("Generated from %s", filepath.Base(filePath))
	}

	return info, nil
}

// fugrMemberGroup returns the function group of an abapGit function group
// member, {group}.fugr.{member}.abap, and "" for any other name. The group's
// own {group}.fugr.abap and vsp's {group}.fugr.{module}.func.abap are matched
// by their own cases before this one is asked.
func fugrMemberGroup(baseName string) string {
	lower := strings.ToLower(baseName)
	if !strings.HasSuffix(lower, ".abap") {
		return ""
	}
	idx := strings.Index(lower, ".fugr.")
	if idx <= 0 {
		return ""
	}
	member := lower[idx+len(".fugr.") : len(lower)-len(".abap")]
	if member == "" || strings.Contains(member, ".") {
		return ""
	}
	return strings.ReplaceAll(strings.ToUpper(baseName[:idx]), "#", "/")
}

// abapObjectName is a repository object name, optionally with a /NAMESPACE/.
var abapObjectName = regexp.MustCompile(`^(/[A-Z0-9_]+/)?[A-Z0-9_]+$`)

// legacyIncludeName names an include from a plain {name}.abap file, the name
// ExportToFile gave includes before issue #235. Anything that is not a plain
// object name is refused rather than guessed at.
func legacyIncludeName(baseName string) (string, error) {
	stem := strings.TrimSuffix(baseName, ".abap")
	name := strings.ToUpper(strings.ReplaceAll(stem, "#", "/"))
	if len(name) > 40 || !abapObjectName.MatchString(name) {
		return "", fmt.Errorf("cannot tell what %s is: no REPORT, PROGRAM, CLASS, INTERFACE, FUNCTION-POOL or FUNCTION statement opens it, and %q is not an include name; name it with its type (.prog.abap, .incl.abap, .clas.abap, .intf.abap, .fugr.abap, {group}.fugr.{module}.abap)", baseName, stem)
	}
	return name, nil
}

// detectTypeFromContent reads the first ABAP statement of a file and says
// what kind of object it opens. It returns "" with no error when the
// statement opens none (an include), and an error when the file is empty or
// opens with a local class or interface, which is ambiguous.
//
// It only reads; it never parses the file as a whole. That is the caller's
// job, done once the type is known.
func detectTypeFromContent(filePath string) (CreatableObjectType, error) {
	stmt, err := firstABAPStatement(filePath)
	if err != nil {
		return "", err
	}
	base := filepath.Base(filePath)
	if stmt == "" {
		return "", fmt.Errorf("could not detect the object type of %s: it holds no ABAP statement", base)
	}
	tokens := strings.Fields(strings.ToUpper(stmt))
	switch tokens[0] {
	case "REPORT", "PROGRAM":
		return ObjectTypeProgram, nil
	case "FUNCTION-POOL":
		return ObjectTypeFunctionGroup, nil
	case "FUNCTION":
		return ObjectTypeFunctionMod, nil
	case "CLASS", "INTERFACE":
		// A global class or interface is declared PUBLIC. Without it the
		// statement declares a local one, which is what a program's include
		// opens with; deploying it as a global object named after the local
		// one would be wrong either way it went, so say so instead.
		for _, t := range tokens[1:] {
			if t == "PUBLIC" {
				if tokens[0] == "CLASS" {
					return ObjectTypeClass, nil
				}
				return ObjectTypeInterface, nil
			}
		}
		return "", fmt.Errorf("could not detect the object type of %s: it opens with a %s that is not PUBLIC, so it may be an include holding a local %s; rename it to .%s.abap if it is the global object, or .incl.abap if it is an include", base, strings.ToLower(tokens[0]), strings.ToLower(tokens[0]), map[string]string{"CLASS": "clas", "INTERFACE": "intf"}[tokens[0]])
	}
	return "", nil
}

// firstABAPStatement returns the text of the first statement in the file, up
// to its period, with comments removed and lines joined. It reads at most the
// first 200 lines.
func firstABAPStatement(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("opening file: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var stmt strings.Builder
	for lineNum := 0; lineNum < 200 && scanner.Scan(); lineNum++ {
		line := scanner.Text()
		if strings.HasPrefix(line, "*") {
			continue // a full-line comment
		}
		if i := strings.Index(line, "\""); i >= 0 {
			line = line[:i] // an end-of-line comment
		}
		if i := strings.Index(line, "."); i >= 0 {
			stmt.WriteString(line[:i])
			return strings.TrimSpace(stmt.String()), nil
		}
		stmt.WriteString(line)
		stmt.WriteString(" ")
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("reading file: %w", err)
	}
	return strings.TrimSpace(stmt.String()), nil
}

// parseClassName extracts class name from CLASS <name> DEFINITION
func parseClassName(line string) string {
	re := regexp.MustCompile(`(?i)^\s*CLASS\s+([a-z0-9_/]+)\s+DEFINITION`)
	matches := re.FindStringSubmatch(line)
	if len(matches) > 1 {
		return strings.ToUpper(matches[1])
	}
	return ""
}

// parseProgramName extracts program name from REPORT/PROGRAM statement
func parseProgramName(line string) string {
	re := regexp.MustCompile(`(?i)^\s*(REPORT|PROGRAM)\s+([a-z0-9_/]+)`)
	matches := re.FindStringSubmatch(line)
	if len(matches) > 2 {
		return strings.ToUpper(matches[2])
	}
	return ""
}

// parseInterfaceName extracts interface name from INTERFACE <name> DEFINITION
func parseInterfaceName(line string) string {
	re := regexp.MustCompile(`(?i)^\s*INTERFACE\s+([a-z0-9_/]+)`)
	matches := re.FindStringSubmatch(line)
	if len(matches) > 1 {
		return strings.ToUpper(matches[1])
	}
	return ""
}

// parseFunctionGroupName extracts function group name from FUNCTION-POOL statement
func parseFunctionGroupName(line string) string {
	re := regexp.MustCompile(`(?i)^\s*FUNCTION-POOL\s+([a-z0-9_/]+)`)
	matches := re.FindStringSubmatch(line)
	if len(matches) > 1 {
		return strings.ToUpper(matches[1])
	}
	return ""
}

// parseFunctionModuleName extracts function module name from FUNCTION statement
func parseFunctionModuleName(line string) string {
	re := regexp.MustCompile(`(?i)^\s*FUNCTION\s+([a-z0-9_/]+)`)
	matches := re.FindStringSubmatch(line)
	if len(matches) > 1 {
		return strings.ToUpper(matches[1])
	}
	return ""
}

// parseDDLSName extracts CDS view name from "define view [entity] <name>" or "@AbapCatalog.viewEnhancementCategory"
func parseDDLSName(line string) string {
	// Pattern: define view [entity] NAME
	re := regexp.MustCompile(`(?i)^\s*define\s+view\s+(?:entity\s+)?([a-z0-9_/]+)`)
	matches := re.FindStringSubmatch(line)
	if len(matches) > 1 {
		return strings.ToUpper(matches[1])
	}
	return ""
}

// parseBDEFName extracts behavior definition name from "define behavior for <name>"
func parseBDEFName(line string) string {
	// Pattern: define behavior for NAME
	re := regexp.MustCompile(`(?i)^\s*define\s+behavior\s+for\s+([a-z0-9_/]+)`)
	matches := re.FindStringSubmatch(line)
	if len(matches) > 1 {
		return strings.ToUpper(matches[1])
	}
	return ""
}

// parseSRVDName extracts service definition name from "define service <name>"
func parseSRVDName(line string) string {
	// Pattern: define service NAME
	re := regexp.MustCompile(`(?i)^\s*define\s+service\s+([a-z0-9_/]+)`)
	matches := re.FindStringSubmatch(line)
	if len(matches) > 1 {
		return strings.ToUpper(matches[1])
	}
	return ""
}

// headerTitleLine is the line SE38's header template puts first: the
// object kind and its name, which is not a description.
var headerTitleLine = regexp.MustCompile(`(?i)^(report|include|program|class|interface|function\s+module|function\s+group)\s+\S+\s*$`)
