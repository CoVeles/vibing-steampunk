package adt

import (
	"context"
	"fmt"
	"html"
	"regexp"
	"sort"
	"strings"
)

// InventoryObject is one TADIR entry of a package.
type InventoryObject struct {
	Type        string `json:"type"`
	Name        string `json:"name"`
	Author      string `json:"author,omitempty"`
	CreatedOn   string `json:"created_on,omitempty"`
	Description string `json:"description,omitempty"`
}

// InventorySubpackage is a package whose parent (TDEVC-PARENTCL) is the
// inventoried one.
type InventorySubpackage struct {
	Name        string `json:"name"`
	Responsible string `json:"responsible,omitempty"`
	CreatedOn   string `json:"created_on,omitempty"`
}

// AbapGitRepo is an abapGit repository registered in table ZABAPGIT.
type AbapGitRepo struct {
	Key       string `json:"key"`
	Package   string `json:"package"`
	URL       string `json:"url,omitempty"`
	Branch    string `json:"branch,omitempty"`
	Offline   bool   `json:"offline"`
	CreatedBy string `json:"created_by,omitempty"`
}

// PackageInventory is what a package holds, in one answer: its TADIR
// objects, its direct subpackages, and the abapGit repository registered for
// it, if any.
type PackageInventory struct {
	Package string `json:"package"`
	// Source names where objects and subpackages were read from.
	Source           string                `json:"source"`
	Objects          []InventoryObject     `json:"objects"`
	ObjectsTruncated bool                  `json:"objects_truncated,omitempty"`
	Subpackages      []InventorySubpackage `json:"subpackages"`
	// AbapGitRepos is nil when it could not be checked (see Skipped/Notes),
	// and empty when it was and none is registered for the package.
	AbapGitRepos []AbapGitRepo `json:"abapgit_repos"`
	Notes        []string      `json:"notes,omitempty"`
	// Skipped says what was not read and why (--block-free-sql).
	Skipped []string `json:"skipped,omitempty"`
}

// inventoryMaxObjects bounds the TADIR read. A variable so tests can make it
// small.
var inventoryMaxObjects = 5000

// packageNamePattern is the shape of a package name. The name goes into an
// SQL literal, so anything else is refused rather than escaped.
var packageNamePattern = regexp.MustCompile(`^[A-Z0-9_$/-]{1,30}$`)

// PackageInventory reads a package's TADIR objects (type, name, author,
// created on), its subpackages (TDEVC) and any abapGit repository registered
// for it, through the data preview. Only reads: SELECTs and the ADT
// package-contents tree.
//
// When free SQL is blocked by the safety configuration (--block-free-sql),
// only the ADT package-contents endpoint is used, and the result says what
// that leaves out. A SELECT that fails (no authorization, a release without a
// column) falls back the same way for that part.
func (c *Client) PackageInventory(ctx context.Context, packageName string) (*PackageInventory, error) {
	pkg := strings.ToUpper(strings.TrimSpace(packageName))
	if !packageNamePattern.MatchString(pkg) {
		return nil, fmt.Errorf("invalid package name %q", packageName)
	}
	inv := &PackageInventory{Package: pkg}

	if err := c.checkSafety(OpFreeSQL, "PackageInventory"); err != nil {
		inv.Source = "ADT package contents (free SQL is blocked)"
		if err := c.inventoryFromADT(ctx, inv, true, true); err != nil {
			return nil, err
		}
		inv.Skipped = append(inv.Skipped,
			"author and created on of each object: they are in TADIR, read with free SQL, which is blocked (--block-free-sql)",
			"responsible and created on of each subpackage: they are in TDEVC, read with free SQL, which is blocked",
			"abapGit repositories: they are in table ZABAPGIT, read with free SQL, which is blocked")
		return inv, nil
	}

	inv.Source = "TADIR, TDEVC (data preview)"
	objectsFromADT, subsFromADT := false, false

	rows, err := c.RunQuery(ctx, fmt.Sprintf(
		"SELECT object, obj_name, author, created_on FROM tadir WHERE pgmid = 'R3TR' AND devclass = '%s'", pkg),
		// One more than is listed, so a package with exactly the limit is
		// not reported as truncated.
		inventoryMaxObjects+1)
	if err != nil {
		inv.Notes = append(inv.Notes, fmt.Sprintf("TADIR could not be read (%v); objects are from the ADT package contents, without author and created on", err))
		objectsFromADT = true
	} else {
		listed := rows.Rows
		if len(listed) > inventoryMaxObjects {
			listed = listed[:inventoryMaxObjects]
			inv.ObjectsTruncated = true
			inv.Notes = append(inv.Notes, fmt.Sprintf("only the first %d TADIR entries are listed", inventoryMaxObjects))
		}
		for _, r := range listed {
			inv.Objects = append(inv.Objects, InventoryObject{
				Type:      rowString(r, "OBJECT"),
				Name:      rowString(r, "OBJ_NAME"),
				Author:    rowString(r, "AUTHOR"),
				CreatedOn: sapDate(rowString(r, "CREATED_ON")),
			})
		}
		if len(rows.Rows) == 0 {
			inv.Notes = append(inv.Notes, "TADIR has no entry for this package, not even its own: it may not exist")
		}
	}

	rows, err = c.RunQuery(ctx, fmt.Sprintf(
		"SELECT devclass, as4user, created_on FROM tdevc WHERE parentcl = '%s'", pkg), 1000)
	if err != nil {
		inv.Notes = append(inv.Notes, fmt.Sprintf("TDEVC could not be read (%v); subpackages are from the ADT package contents", err))
		subsFromADT = true
	} else {
		for _, r := range rows.Rows {
			inv.Subpackages = append(inv.Subpackages, InventorySubpackage{
				Name:        rowString(r, "DEVCLASS"),
				Responsible: rowString(r, "AS4USER"),
				CreatedOn:   sapDate(rowString(r, "CREATED_ON")),
			})
		}
	}

	if objectsFromADT || subsFromADT {
		if err := c.inventoryFromADT(ctx, inv, objectsFromADT, subsFromADT); err != nil {
			inv.Notes = append(inv.Notes, fmt.Sprintf("ADT package contents could not be read either: %v", err))
		}
	}

	c.inventoryAbapGit(ctx, inv)
	sortInventory(inv)
	return inv, nil
}

// inventoryFromADT fills objects and/or subpackages from the ADT
// package-contents tree (nodestructure), which has neither author nor date.
func (c *Client) inventoryFromADT(ctx context.Context, inv *PackageInventory, objects, subs bool) error {
	content, err := c.GetPackage(ctx, inv.Package)
	if err != nil {
		return err
	}
	if objects {
		for _, o := range content.Objects {
			typ := o.Type
			if i := strings.IndexByte(typ, '/'); i > 0 {
				typ = typ[:i] // CLAS/OC -> CLAS, the TADIR object type
			}
			inv.Objects = append(inv.Objects, InventoryObject{Type: typ, Name: o.Name, Description: o.Description})
		}
	}
	if subs {
		for _, s := range content.SubPackages {
			inv.Subpackages = append(inv.Subpackages, InventorySubpackage{Name: s})
		}
	}
	sortInventory(inv)
	return nil
}

var (
	abapGitTag = func(tag string) *regexp.Regexp {
		return regexp.MustCompile(`<` + tag + `>([^<]*)</` + tag + `>`)
	}
	abapGitPackage  = abapGitTag("PACKAGE")
	abapGitURL      = abapGitTag("URL")
	abapGitBranch   = abapGitTag("BRANCH_NAME")
	abapGitOffline  = abapGitTag("OFFLINE")
	abapGitCreateBy = abapGitTag("CREATED_BY")
)

// inventoryAbapGit looks for an abapGit repository registered for the
// package. abapGit keeps them in its own table ZABAPGIT (TYPE 'REPO', the
// repository as XML in DATA_STR); a system without abapGit has no such table,
// so its existence is checked in DD02L first instead of letting the SELECT
// fail.
func (c *Client) inventoryAbapGit(ctx context.Context, inv *PackageInventory) {
	rows, err := c.RunQuery(ctx, "SELECT tabname FROM dd02l WHERE tabname = 'ZABAPGIT' AND as4local = 'A'", 1)
	if err != nil {
		inv.Notes = append(inv.Notes, fmt.Sprintf("abapGit: could not check for table ZABAPGIT (%v)", err))
		return
	}
	if len(rows.Rows) == 0 {
		inv.AbapGitRepos = []AbapGitRepo{}
		inv.Notes = append(inv.Notes, "abapGit: not installed (no table ZABAPGIT)")
		return
	}
	rows, err = c.RunQuery(ctx, "SELECT value, data_str FROM zabapgit WHERE type = 'REPO'", 1000)
	if err != nil {
		inv.Notes = append(inv.Notes, fmt.Sprintf("abapGit: table ZABAPGIT could not be read (%v)", err))
		return
	}
	inv.AbapGitRepos = []AbapGitRepo{}
	for _, r := range rows.Rows {
		repo, ok := parseAbapGitRepo(rowString(r, "VALUE"), rowString(r, "DATA_STR"))
		if ok && strings.EqualFold(repo.Package, inv.Package) {
			inv.AbapGitRepos = append(inv.AbapGitRepos, repo)
		}
	}
}

// parseAbapGitRepo reads the fields of one ZABAPGIT REPO row. DATA_STR is
// asXML declared as UTF-16 though it arrives as UTF-8 text, which
// encoding/xml refuses, so the few flat fields needed are read by tag.
func parseAbapGitRepo(key, data string) (AbapGitRepo, bool) {
	tag := func(re *regexp.Regexp) string {
		if m := re.FindStringSubmatch(data); m != nil {
			return strings.TrimSpace(html.UnescapeString(m[1]))
		}
		return ""
	}
	repo := AbapGitRepo{
		Key:       strings.TrimSpace(key),
		Package:   tag(abapGitPackage),
		URL:       tag(abapGitURL),
		Branch:    tag(abapGitBranch),
		Offline:   tag(abapGitOffline) == "X",
		CreatedBy: tag(abapGitCreateBy),
	}
	return repo, repo.Package != ""
}

func sortInventory(inv *PackageInventory) {
	sort.SliceStable(inv.Objects, func(i, j int) bool {
		if inv.Objects[i].Type != inv.Objects[j].Type {
			return inv.Objects[i].Type < inv.Objects[j].Type
		}
		return inv.Objects[i].Name < inv.Objects[j].Name
	})
	sort.SliceStable(inv.Subpackages, func(i, j int) bool { return inv.Subpackages[i].Name < inv.Subpackages[j].Name })
	if inv.Objects == nil {
		inv.Objects = []InventoryObject{}
	}
	if inv.Subpackages == nil {
		inv.Subpackages = []InventorySubpackage{}
	}
}

// sapDate turns 20251218 into 2025-12-18; an initial date is empty.
func sapDate(s string) string {
	switch {
	case s == "00000000":
		return ""
	case len(s) == 8:
		return s[:4] + "-" + s[4:6] + "-" + s[6:]
	}
	return s
}
