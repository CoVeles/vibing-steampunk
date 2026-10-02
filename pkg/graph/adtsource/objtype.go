package adtsource

import "strings"

// MainType returns the repository object type an ADT type code names: the part
// before the slash, upper-cased. A package listing does not say "CLAS"; it says
// "CLAS/OC", "PROG/P", "INTF/OI". Comparing that with the bare type matches
// nothing, and a collector that skips every object reads no source and reports
// an empty package, which is a verdict about code nobody opened.
func MainType(adtType string) string {
	main, _, _ := strings.Cut(strings.ToUpper(strings.TrimSpace(adtType)), "/")
	return main
}
