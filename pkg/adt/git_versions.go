package adt

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// --- object versions: what git_delete_objects compares under the lock -------
//
// A caller that decides in one call and deletes in another reads the
// versions first (git_object_versions) and passes them back as each item's
// expect. git_delete_objects then takes the ADT lock, reads the version
// again while it holds the lock, and deletes only when it still matches.
//
// stamp (ZCL_VSP_GIT_SERVICE=>object_stamp):
//
//	v1:<TABLE>:<YYYYMMDDHHMMSS>:<ROWS>
//
// the newest change date and time over every version row of the object,
// active and inactive, and the number of those rows. CLAS and INTF: REPOSRC
// UDAT/UTIME over every include of the pool (the name padded with '=' to 30
// characters, then any suffix); PROG: REPOSRC of the program; TABL DD02L,
// DTEL DD04L, DOMA DD01L, TTYP DD40L, DDLS DDDDLSRC, AS4DATE/AS4TIME. Its
// resolution is a second: two changes within the second the stamp was read
// in look alike; sha256 does not have that limit.
//
// sha256 (ZCL_VSP_GIT_SERVICE=>object_sha256): lower-case hex SHA-256 of the
// UTF-8 text of the lines "<file name>=<lower-case hex SHA-256 of the
// file>", one per file of the object's abapGit serialisation in its original
// language only (TADIR-MASTERLANG), sorted (byte order), joined by LF,
// without a final LF.

// GitExpect is the version of an object a caller saw. Either one matching
// is enough; one that cannot be read, or is missing, never matches.
type GitExpect struct {
	Stamp  string `json:"stamp,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
}

// GitRepoExpect is the repository row a caller saw for the package.
type GitRepoExpect struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

// GitDeleteOptions are DeleteGitObjectsWith's options.
type GitDeleteOptions struct {
	Transport  string
	DeleteRepo bool
	// ExpectRepo, with DeleteRepo: drop the repository row only when it is
	// exactly this one.
	ExpectRepo *GitRepoExpect
}

// GitChangedError says an object is no longer the version the caller
// expected; it was kept.
type GitChangedError struct {
	Type, Name string
	Observed   GitExpect
}

func (e *GitChangedError) Error() string {
	var seen []string
	if e.Observed.Stamp != "" {
		seen = append(seen, "stamp "+e.Observed.Stamp)
	}
	if e.Observed.SHA256 != "" {
		seen = append(seen, "sha256 "+e.Observed.SHA256)
	}
	what := "it has no version any more"
	if len(seen) > 0 {
		what = "it is now " + strings.Join(seen, ", ")
	}
	return fmt.Sprintf("changed since its version was read (%s); not deleted", what)
}

// gitStampTables is the version table of each type that has a stamp.
var gitStampTables = map[string]string{
	"CLAS": "REPOSRC", "INTF": "REPOSRC", "PROG": "REPOSRC",
	"TABL": "DD02L", "DTEL": "DD04L", "DOMA": "DD01L", "TTYP": "DD40L", "DDLS": "DDDDLSRC",
}

var (
	gitStampRe  = regexp.MustCompile(`^v1:([A-Z0-9]+):([0-9]{14}):([0-9]+)$`)
	gitSHA256Re = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// GitStampTypes lists the TADIR types that have a stamp.
func GitStampTypes() string { return "CLAS, INTF, PROG, TABL, DTEL, DOMA, TTYP, DDLS" }

// normalized checks an expectation for an object of objType: at least one of
// stamp and sha256, each well formed, a stamp of a type that has one and of
// that type's table. It never drops an expectation it cannot check.
func (e *GitExpect) normalized(objType string) (*GitExpect, error) {
	if e == nil {
		return nil, nil
	}
	out := &GitExpect{Stamp: strings.TrimSpace(e.Stamp), SHA256: strings.ToLower(strings.TrimSpace(e.SHA256))}
	if out.Stamp == "" && out.SHA256 == "" {
		return nil, errors.New("expect needs stamp or sha256 (read them with git_object_versions)")
	}
	if out.Stamp != "" {
		table, ok := gitStampTables[strings.ToUpper(objType)]
		if !ok {
			return nil, fmt.Errorf("expect stamp: type %s has no stamp (only %s); use sha256", objType, GitStampTypes())
		}
		m := gitStampRe.FindStringSubmatch(out.Stamp)
		if m == nil {
			return nil, fmt.Errorf("expect stamp %q is not v1:<TABLE>:<YYYYMMDDHHMMSS>:<ROWS> as git_object_versions reports it", out.Stamp)
		}
		if m[1] != table {
			return nil, fmt.Errorf("expect stamp %q is of %s; a %s stamp is of %s", out.Stamp, m[1], objType, table)
		}
	}
	if out.SHA256 != "" && !gitSHA256Re.MatchString(out.SHA256) {
		return nil, fmt.Errorf("expect sha256 %q is not 64 hex digits", e.SHA256)
	}
	return out, nil
}

// ParseGitExpect reads an item's expect: {"stamp": "...", "sha256": "..."},
// either or both. Any other key, or a value that is not a string, is refused.
func ParseGitExpect(objType string, raw any) (*GitExpect, error) {
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("expect must be {\"stamp\": ..., \"sha256\": ...}, not %T", raw)
	}
	var e GitExpect
	for k, v := range m {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("expect %s must be a string, not %T", k, v)
		}
		switch k {
		case "stamp":
			e.Stamp = s
		case "sha256":
			e.SHA256 = s
		default:
			return nil, fmt.Errorf("expect: unknown key %q (stamp, sha256)", k)
		}
	}
	return e.normalized(objType)
}

// sameGitExpect says whether two expectations are the same (both absent too).
func sameGitExpect(a, b *GitExpect) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// ApplyGitExpectFlags attaches the CLI's --expect values to items: each
// "TYPE NAME stamp=<v>" or "TYPE NAME sha256=<h>" (both may follow one
// object). An --expect for an object that is not among items is refused.
func ApplyGitExpectFlags(items []GitDeleteItem, flags []string) error {
	for _, f := range flags {
		fields := strings.Fields(f)
		if len(fields) < 3 {
			return fmt.Errorf("--expect %q is not \"TYPE NAME stamp=<v>\" or \"TYPE NAME sha256=<h>\"", f)
		}
		typ, name := strings.ToUpper(fields[0]), strings.ToUpper(fields[1])
		raw := map[string]any{}
		for _, kv := range fields[2:] {
			k, v, ok := strings.Cut(kv, "=")
			if !ok || v == "" {
				return fmt.Errorf("--expect %q: %q is not stamp=<v> or sha256=<h>", f, kv)
			}
			if _, dup := raw[k]; dup {
				return fmt.Errorf("--expect %q: %s given twice", f, k)
			}
			raw[k] = v
		}
		exp, err := ParseGitExpect(typ, raw)
		if err != nil {
			return fmt.Errorf("--expect %q: %w", f, err)
		}
		found := false
		for i := range items {
			if items[i].Type == typ && items[i].Name == name {
				if items[i].Expect != nil && !sameGitExpect(items[i].Expect, exp) {
					return fmt.Errorf("--expect %q: %s %s has another --expect already", f, typ, name)
				}
				items[i].Expect, found = exp, true
			}
		}
		if !found {
			return fmt.Errorf("--expect %q: %s %s is not among the objects to delete", f, typ, name)
		}
	}
	return nil
}

// normalized checks an expected repository: only with deleteRepo, key and
// name both given.
func (r *GitRepoExpect) normalized(deleteRepo bool) (*GitRepoExpect, error) {
	if r == nil {
		return nil, nil
	}
	out := &GitRepoExpect{Key: strings.TrimSpace(r.Key), Name: strings.TrimSpace(r.Name)}
	if !deleteRepo {
		return nil, errors.New("expect_repo is only for delete_repo: the repository is kept without it")
	}
	if out.Key == "" || out.Name == "" {
		return nil, errors.New("expect_repo needs key and name: the repository row as git_object_versions or the package's contents reported it")
	}
	return out, nil
}

// matches says whether repo is exactly the expected row.
func (r *GitRepoExpect) matches(repo *GitRepoInfo) bool {
	return repo != nil && repo.Key == r.Key && repo.Name == r.Name
}

// GitObjectVersion is an object's version as ZADT_VSP reads it.
type GitObjectVersion struct {
	Type string `json:"type"`
	Name string `json:"name"`
	// Package is the object's TADIR package (empty: no TADIR entry).
	Package string `json:"package"`
	// InPackage: the object is in the package asked about. Only then is
	// there a version.
	InPackage   bool   `json:"inPackage"`
	Stamp       string `json:"stamp,omitempty"`
	StampError  string `json:"stampError,omitempty"`
	SHA256      string `json:"sha256,omitempty"`
	SHA256Error string `json:"sha256Error,omitempty"`
	// Files is the number of files the sha256 is over.
	Files int `json:"files,omitempty"`
}

type gitVersionsAnswer struct {
	Objects []struct {
		Type        string `json:"type"`
		Name        string `json:"name"`
		Devclass    string `json:"devclass"`
		InPackage   bool   `json:"in_package"`
		Stamp       string `json:"stamp"`
		StampError  string `json:"stamp_error"`
		SHA256      string `json:"sha256"`
		SHA256Error string `json:"sha256_error"`
		Files       int    `json:"files"`
	} `json:"objects"`
}

// GitMaxVersions is how many objects one GitObjectVersions call may name
// (ZCL_VSP_GIT_SERVICE=>c_max_versions).
const GitMaxVersions = 500

// GitObjectVersions reads the stamp -- and with withSHA the sha256 -- of
// objects of pkg through ZADT_VSP. It changes nothing. Each answer is for
// the item at the same index; an answer that does not say which object it
// is, or is missing, is an error.
func (c *Client) GitObjectVersions(ctx context.Context, ws GitService, pkg string, items []GitDeleteItem, withSHA bool) ([]GitObjectVersion, error) {
	if err := c.checkSafety(OpRead, "GitObjectVersions"); err != nil {
		return nil, err
	}
	p, err := NormalizeGitPackage(pkg)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, errors.New("objects is empty: name the objects to read")
	}
	if len(items) > GitMaxVersions {
		return nil, fmt.Errorf("%d objects: at most %d per call", len(items), GitMaxVersions)
	}
	names := make([]string, 0, len(items))
	for _, it := range items {
		if !gitObjTypeRe.MatchString(it.Type) || it.Name == "" || len(it.Name) > 40 || strings.ContainsAny(it.Name, ", \"\\") {
			return nil, fmt.Errorf("object %s %s is not a TADIR type and name", it.Type, it.Name)
		}
		names = append(names, it.Type+" "+it.Name)
	}
	params := map[string]any{"package": p, "objects": strings.Join(names, ",")}
	if withSHA {
		params["sha256"] = "true"
	}
	var a gitVersionsAnswer
	if err := gitCall(ctx, ws, "object_versions", params, 5*time.Minute, &a); err != nil {
		return nil, err
	}
	if len(a.Objects) != len(items) {
		return nil, fmt.Errorf("ZADT_VSP answered %d versions for %d objects", len(a.Objects), len(items))
	}
	t := strings.TrimSpace
	out := make([]GitObjectVersion, 0, len(items))
	for i, o := range a.Objects {
		v := GitObjectVersion{Type: strings.ToUpper(t(o.Type)), Name: strings.ToUpper(t(o.Name)), Package: t(o.Devclass), InPackage: o.InPackage,
			Stamp: t(o.Stamp), StampError: t(o.StampError), SHA256: strings.ToLower(t(o.SHA256)), SHA256Error: t(o.SHA256Error), Files: o.Files}
		if v.Type != items[i].Type || v.Name != items[i].Name {
			return nil, fmt.Errorf("ZADT_VSP answered %s %s where %s %s was asked", v.Type, v.Name, items[i].Type, items[i].Name)
		}
		if v.InPackage && !strings.EqualFold(v.Package, p) {
			return nil, fmt.Errorf("ZADT_VSP says %s %s is in package %s and in %s", v.Type, v.Name, v.Package, p)
		}
		out = append(out, v)
	}
	return out, nil
}

// checkGitExpect reads item's version and compares it with item.Expect. It
// runs while the ADT lock is held. It returns what was observed, and nil
// only on a match: a *GitChangedError when the object is another version (or
// has none), any other error when the version could not be read -- never
// nil for an uncertain comparison.
func (c *Client) checkGitExpect(ctx context.Context, ws GitService, pkg string, item GitDeleteItem) (*GitExpect, error) {
	exp := item.Expect
	if exp == nil {
		return nil, errors.New("no expectation to check")
	}
	vs, err := c.GitObjectVersions(ctx, ws, pkg, []GitDeleteItem{{Type: item.Type, Name: item.Name}}, exp.SHA256 != "")
	if err != nil {
		return nil, fmt.Errorf("its version could not be read under the lock, so it was not deleted: %w", err)
	}
	v := vs[0]
	if !v.InPackage {
		where := "it has no TADIR entry any more"
		if v.Package != "" {
			where = "it is in package " + v.Package + " now"
		}
		return nil, fmt.Errorf("%s, not %s; not deleted", where, pkg)
	}
	obs := &GitExpect{}
	if exp.Stamp != "" {
		obs.Stamp = v.Stamp
	}
	if exp.SHA256 != "" {
		obs.SHA256 = v.SHA256
	}
	if (exp.Stamp != "" && v.Stamp != "" && v.Stamp == exp.Stamp) || (exp.SHA256 != "" && v.SHA256 != "" && v.SHA256 == exp.SHA256) {
		return obs, nil
	}
	// No match. A part that could not be read leaves it uncertain: failed,
	// not changed -- and never deleted.
	var unread []string
	if exp.Stamp != "" && v.StampError != "" {
		unread = append(unread, "stamp: "+v.StampError)
	}
	if exp.SHA256 != "" && v.SHA256Error != "" {
		unread = append(unread, "sha256: "+v.SHA256Error)
	}
	if len(unread) > 0 {
		return obs, fmt.Errorf("its version could not be read (%s), so it was not deleted", strings.Join(unread, "; "))
	}
	return obs, &GitChangedError{Type: item.Type, Name: item.Name, Observed: *obs}
}
