package adt

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// --- Writing one include of a class (issue #242) ---
//
// A class include (testclasses, definitions, implementations, macros) is
// addressed as /oo/classes/<name>/includes/<include>, with no /source/main
// suffix; the main source is /oo/classes/<name>/source/main. Both are locked
// through the class. Writing an include through the main-source path lands
// the include's code in the main source, which is how #242 was found: a test
// class sent with include=testclasses was saved as the global class body.

// ParseClassIncludeType maps the include name a caller supplies to the include
// it addresses. Empty and "main" both mean the main source. Any other name is
// refused rather than mapped to the main source, so a typo cannot overwrite
// the class.
func ParseClassIncludeType(include string) (ClassIncludeType, error) {
	switch ClassIncludeType(strings.ToLower(strings.TrimSpace(include))) {
	case "", ClassIncludeMain:
		return ClassIncludeMain, nil
	case ClassIncludeDefinitions:
		return ClassIncludeDefinitions, nil
	case ClassIncludeImplementations:
		return ClassIncludeImplementations, nil
	case ClassIncludeMacros:
		return ClassIncludeMacros, nil
	case ClassIncludeTestClasses:
		return ClassIncludeTestClasses, nil
	}
	return "", fmt.Errorf("unknown class include %q (supported: main, definitions, implementations, macros, testclasses)", include)
}

// SplitClassIncludeURL recognises the URL of a class include,
// /sap/bc/adt/oo/classes/<name>/includes/<include>. For such a URL it returns
// the class URL, which is what has to be locked, and the URL the include's
// source is written to. The class segment is kept exactly as given, so a
// namespaced name the caller already escaped is not escaped a second time
// (#282).
//
// ok is false for any other URL, including program includes
// (/programs/includes/<name>), whose source does live under /source/main.
// An include name that is not a known class include is an error, never a
// fallback to the main source.
func SplitClassIncludeURL(objectURL string) (classURL, sourceURL string, ok bool, err error) {
	const classesPrefix = "/sap/bc/adt/oo/classes/"
	trimmed := strings.TrimSuffix(objectURL, "/")
	if !strings.HasPrefix(strings.ToLower(trimmed), classesPrefix) {
		return "", "", false, nil
	}
	parts := strings.Split(trimmed[len(classesPrefix):], "/")
	if len(parts) < 2 || parts[0] == "" || !strings.EqualFold(parts[1], "includes") {
		return "", "", false, nil
	}

	classURL = trimmed[:len(classesPrefix)+len(parts[0])]
	if len(parts) != 3 || parts[2] == "" {
		return classURL, "", true, fmt.Errorf("class include URL %s does not name exactly one include (supported: definitions, implementations, macros, testclasses)", objectURL)
	}
	include, err := ParseClassIncludeType(parts[2])
	if err != nil {
		return classURL, "", true, err
	}
	if include == ClassIncludeMain {
		// includes/main does not answer on SAP; the main source is /source/main.
		return classURL, classURL + "/source/main", true, nil
	}
	return classURL, classURL + "/includes/" + string(include), true, nil
}

// writeClassIncludeUpdate is the WriteSource branch for CLAS with an include
// other than main: gate on the class, lock the class, write the include's own
// source URL, unlock, activate the class. The testclasses include is created
// when it does not exist yet, as the test_source path already does.
func (c *Client) writeClassIncludeUpdate(ctx context.Context, name string, include ClassIncludeType, source string, opts *WriteSourceOptions) (*WriteSourceResult, error) {
	name = strings.ToUpper(unescapeObjectName(name))
	objectURL := GetObjectURL(ObjectTypeClass, name, "")
	result := &WriteSourceResult{
		ObjectType: "CLAS",
		ObjectName: name,
		ObjectURL:  objectURL,
		Include:    string(include),
		Mode:       "updated",
	}
	opName := "WriteSource(" + string(include) + ")"

	// The full gate (operation type, package of the class, transport policy),
	// run above the lock so nothing inside the lock window repeats the
	// stateless package lookup (#91).
	ctx, err := c.gateAndMark(ctx, MutationContext{
		Op:        OpUpdate,
		OpName:    opName,
		ObjectURL: objectURL,
		Transport: opts.Transport,
	})
	if err != nil {
		return nil, err
	}
	if opts.ExpectedSourceHash != "" {
		ctx = withExpectedSourceHash(ctx, opts.ExpectedSourceHash)
	}

	trPlan := c.planTransport(ctx, opts.Transport, objectURL, "")
	lock, err := c.LockObject(ctx, objectURL, "MODIFY", trPlan.lockCorrNr(opts.Transport))
	if err != nil {
		result.Message = fmt.Sprintf("Failed to lock class %s: %v", name, err)
		return result, nil
	}

	failUnderLock := func(message string) (*WriteSourceResult, error) {
		result.Message = message
		if unlockErr := c.releaseLockAfterFailure(ctx, objectURL, lock.LockHandle); unlockErr != nil {
			result.Message += " (" + strandedLockAdvice(objectURL, unlockErr) + ")"
		}
		return result, nil
	}

	transport, trNote, err := c.resolveWriteTransportFor(trPlan, opts.Transport, lock.CorrNr, opName)
	if err != nil {
		return failUnderLock(fmt.Sprintf("Transportable-edit check failed: %v", err))
	}
	result.Transport, result.TransportNote = transport, trNote

	createdInclude := false
	err = c.UpdateClassInclude(ctx, name, include, source, lock.LockHandle, transport)
	if err != nil && include == ClassIncludeTestClasses && testIncludeMissing(err) {
		// A class has no testclasses include until one is created.
		if createErr := c.CreateTestInclude(ctx, name, lock.LockHandle, transport); createErr != nil {
			err = fmt.Errorf("%w; creating the testclasses include also failed: %v", err, createErr)
		} else {
			createdInclude = true
			err = c.UpdateClassInclude(ctx, name, include, source, lock.LockHandle, transport)
		}
	}
	if err != nil {
		return failUnderLock(fmt.Sprintf("Failed to update the %s include of %s: %v", include, name, err))
	}

	if err := c.UnlockObject(ctx, objectURL, lock.LockHandle); err != nil {
		result.Message = fmt.Sprintf("The %s include was written, but unlocking %s failed: %v", include, name, err)
		return result, nil
	}

	activation, err := c.Activate(ctx, objectURL, name)
	result.Activation = activation
	if err != nil {
		result.Message = fmt.Sprintf("The %s include was written but not activated: %v", include, err)
		return result, nil
	}
	if activation == nil || !activation.Success {
		result.Message = fmt.Sprintf("The %s include was written, but activation failed - check activation messages", include)
		return result, nil
	}

	result.Success = true
	result.Message = fmt.Sprintf("The %s include of %s was updated and activated", include, name)
	if createdInclude {
		result.Message = fmt.Sprintf("The %s include of %s was created, written and activated", include, name)
	}
	return result, nil
}

// testIncludeMissing reports whether a write to the testclasses include failed
// because the class has no such include yet. A 7.58 does not answer that with
// a 404: the PUT comes back 500 ExceptionResourceSaveFailure, message ED 170
// "<class>====CCAU does not have any inactive version". The T100 key is
// matched as well as the English text, so a logon language other than English
// is recognised too.
func testIncludeMissing(err error) bool {
	if IsNotFoundError(err) {
		return true
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	msg := apiErr.Message
	if strings.Contains(msg, `"T100KEY-ID">ED<`) && strings.Contains(msg, `"T100KEY-NO">170<`) {
		return true
	}
	return strings.Contains(msg, "does not have any inactive version")
}
