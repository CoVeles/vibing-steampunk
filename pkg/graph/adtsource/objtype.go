package adtsource

import "strings"

// SourceKind names the kind of source-bearing object an ADT type code stands
// for, in the terms adt.Client.GetSource takes, or "" when the code is not one
// of them.
//
// A package listing does not say "CLAS"; it says "CLAS/OC". Comparing that with
// the bare type matched nothing, and a collector that skipped every object
// reported an empty package. Cutting the code at the slash is the wrong fix,
// though, because the part after the slash matters: PROG/I is an include, not a
// program, and is read at a different resource; FUGR/FF is one function module,
// not its group; TABL/DS is a structure and TABL/DT a table. So the codes are
// mapped one by one, and a code not listed here is not guessed at.
//
// Bare codes, as TADIR gives them, map to themselves.
func SourceKind(adtType string) string {
	switch strings.ToUpper(strings.TrimSpace(adtType)) {
	case "CLAS", "CLAS/OC":
		return "CLAS"
	case "INTF", "INTF/OI":
		return "INTF"
	case "PROG", "PROG/P":
		return "PROG"
	case "INCL", "PROG/I":
		return "INCL"
	case "FUGR", "FUGR/F":
		return "FUGR"
	case "FUNC", "FUGR/FF":
		return "FUNC"
	}
	return ""
}
