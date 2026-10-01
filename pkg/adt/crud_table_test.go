package adt

import (
	"strings"
	"testing"
)

// --- create TABL: the client field and the field attributes (issue #254) ---

func boolPtr(b bool) *bool { return &b }

func tableOpts(fields ...TableField) CreateTableOptions {
	return CreateTableOptions{
		Name:          "ZDEMO_T",
		Description:   "demo",
		Package:       "$TMP",
		DeliveryClass: "A",
		TableCategory: "TRANSPARENT",
		Fields:        fields,
	}
}

func clientLines(ddl string) []string {
	var out []string
	for _, line := range strings.Split(ddl, "\n") {
		if strings.Contains(line, "abap.clnt") || strings.Contains(line, ": mandt") {
			out = append(out, strings.TrimSpace(line))
		}
	}
	return out
}

func TestGenerateTableDDL_ClientField(t *testing.T) {
	mandt := TableField{Name: "MANDT", Type: "MANDT", IsKey: true}
	id := TableField{Name: "ID", Type: "CHAR10", IsKey: true}
	val := TableField{Name: "VAL", Type: "INT4"}

	tests := []struct {
		name       string
		opts       CreateTableOptions
		wantClient []string // the client-typed lines, in order
		wantErr    string
	}{
		{
			// The issue's call: the caller's own MANDT is the client field,
			// and no second one is put in front of it.
			name:       "first key field MANDT is the client field",
			opts:       tableOpts(mandt, id, val),
			wantClient: []string{"key mandt : mandt not null;"},
		},
		{
			name:       "built-in CLNT is a client field too",
			opts:       tableOpts(TableField{Name: "MANDT", Type: "CLNT", IsKey: true}, id),
			wantClient: []string{"key mandt : abap.clnt not null;"},
		},
		{
			// Backward compatible default: no client field in the list, so
			// vsp adds one as it always has.
			name:       "no client field: CLIENT is added in front",
			opts:       tableOpts(id, val),
			wantClient: []string{"key client : abap.clnt not null;"},
		},
		{
			name: "client_dependent true without a client field adds one",
			opts: func() CreateTableOptions {
				o := tableOpts(id, val)
				o.ClientDependent = boolPtr(true)
				return o
			}(),
			wantClient: []string{"key client : abap.clnt not null;"},
		},
		{
			name: "client_dependent false: no client field",
			opts: func() CreateTableOptions {
				o := tableOpts(id, val)
				o.ClientDependent = boolPtr(false)
				return o
			}(),
			wantClient: nil,
		},
		{
			name: "client_dependent false with a MANDT first key is contradictory",
			opts: func() CreateTableOptions {
				o := tableOpts(mandt, id)
				o.ClientDependent = boolPtr(false)
				return o
			}(),
			wantErr: "client_dependent is false",
		},
		{
			name:    "a client field that is not the first key is refused",
			opts:    tableOpts(id, mandt),
			wantErr: "not the first key field",
		},
		{
			name:    "a non-key MANDT field is refused rather than doubled",
			opts:    tableOpts(TableField{Name: "MANDT", Type: "MANDT"}, id),
			wantErr: "not the first key field",
		},
		{
			name:    "a field named CLIENT would collide with the added one",
			opts:    tableOpts(id, TableField{Name: "client", Type: "CHAR3"}),
			wantErr: "collides",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ddl, err := generateTableDDL(tt.opts)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("want an error containing %q, got DDL:\n%s", tt.wantErr, ddl)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error %q does not contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("generateTableDDL: %v", err)
			}
			got := clientLines(ddl)
			if strings.Join(got, "|") != strings.Join(tt.wantClient, "|") {
				t.Errorf("client lines = %q, want %q; DDL:\n%s", got, tt.wantClient, ddl)
			}
		})
	}
}

func TestParseTableFields_RefusesUnknownAttributes(t *testing.T) {
	_, err := ParseTableFields(`[{"name":"MANDT","type":"MANDT","key":true,"not_null":true}]`)
	if err == nil {
		t.Fatal(`"not_null" was accepted; it used to be dropped without a word`)
	}
	for _, want := range []string{`"not_null"`, `did you mean "notNull"`, "MANDT", "field 1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}

	for _, js := range []string{
		`[{"name":"A","type":"INT4","notnull":true}]`,
		`[{"name":"A","type":"INT4","is_key":true}]`,
		`[{"name":"A","type":"INT4","nullable":false}]`,
	} {
		if _, err := ParseTableFields(js); err == nil {
			t.Errorf("%s: unknown attribute accepted", js)
		}
	}
}

func TestParseTableFields_AcceptsKnownAttributes(t *testing.T) {
	fields, err := ParseTableFields(`[
		{"name":"MANDT","type":"MANDT","key":true},
		{"name":"AMT","type":"DEC","length":15,"decimals":2,"description":"amount","notNull":true}
	]`)
	if err != nil {
		t.Fatalf("ParseTableFields: %v", err)
	}
	if len(fields) != 2 {
		t.Fatalf("got %d fields, want 2", len(fields))
	}
	if !fields[0].IsKey || fields[0].Name != "MANDT" {
		t.Errorf("field 1 = %+v", fields[0])
	}
	f := fields[1]
	if !f.NotNull || f.Length != 15 || f.Decimals != 2 || f.Description != "amount" {
		t.Errorf("field 2 = %+v", f)
	}
}

func TestParseTableFields_RequiresNameAndType(t *testing.T) {
	for _, js := range []string{`[{"type":"INT4"}]`, `[{"name":"A"}]`, `[null]`, `{"name":"A"}`} {
		if _, err := ParseTableFields(js); err == nil {
			t.Errorf("%s: accepted", js)
		}
	}
}
