package mcp

import "testing"

// Callers reach usage examples through the where-used list. Since #281 a
// function module arrives as itself, namespaced ones escaped in the URI, and
// an include whose program could not be read arrives as an include. Neither
// may be dropped or renamed on the way to a source read.
func TestUsageTypeNameFromURIReadsModulesAndIncludes(t *testing.T) {
	cases := []struct {
		uri, name                   string
		wantType, wantName, wantGrp string
	}{
		{
			uri:      "/sap/bc/adt/functions/groups/%2fsdf%2fewa/fmodules/%2fsdf%2fewa_sdccn",
			name:     "/SDF/EWA_SDCCN",
			wantType: "FUNC", wantName: "/SDF/EWA_SDCCN", wantGrp: "/SDF/EWA",
		},
		{
			uri:      "/sap/bc/adt/functions/groups/ups_c/fmodules/ups_send_start_mail",
			name:     "UPS_SEND_START_MAIL",
			wantType: "FUNC", wantName: "UPS_SEND_START_MAIL", wantGrp: "UPS_C",
		},
		{
			uri:      "/sap/bc/adt/programs/includes/zdemo_orphan_incl",
			name:     "ZDEMO_ORPHAN_INCL",
			wantType: "INCL", wantName: "ZDEMO_ORPHAN_INCL",
		},
		{
			uri:      "/sap/bc/adt/programs/programs/zdemo_report",
			name:     "ZDEMO_REPORT",
			wantType: "PROG", wantName: "ZDEMO_REPORT",
		},
	}
	for _, tc := range cases {
		typ, name, grp := usageTypeNameFromURI(tc.uri, tc.name)
		if typ != tc.wantType || name != tc.wantName || grp != tc.wantGrp {
			t.Errorf("%s: got (%q, %q, %q), want (%q, %q, %q)",
				tc.uri, typ, name, grp, tc.wantType, tc.wantName, tc.wantGrp)
		}
	}
}
