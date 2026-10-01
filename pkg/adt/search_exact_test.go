package adt

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSearchObjectExact(t *testing.T) {
	var gotQuery, gotType, gotMax string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("query")
		gotType = r.URL.Query().Get("objectType")
		gotMax = r.URL.Query().Get("maxResults")
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<?xml version="1.0" encoding="utf-8"?><adtcore:objectReferences xmlns:adtcore="http://www.sap.com/adt/core">` +
			`<adtcore:objectReference adtcore:uri="/sap/bc/adt/oo/classes/zcl_order_helper" adtcore:type="CLAS/OC" adtcore:name="ZCL_ORDER_HELPER"/>` +
			`<adtcore:objectReference adtcore:uri="/sap/bc/adt/oo/classes/zcl_order" adtcore:type="CLAS/OC" adtcore:name="ZCL_ORDER"/>` +
			`<adtcore:objectReference adtcore:uri="/sap/bc/adt/programs/programs/zcl_order" adtcore:type="PROG/P" adtcore:name="zcl_order"/>` +
			`<adtcore:objectReference adtcore:uri="/sap/bc/adt/oo/classes/zcl_orders" adtcore:type="CLAS/OC" adtcore:name="ZCL_ORDERS"/>` +
			`</adtcore:objectReferences>`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "u", "p")

	got, err := c.SearchObjectExact(context.Background(), "zcl_order", "CLAS", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "ZCL_ORDER" || got[1].Name != "zcl_order" {
		t.Fatalf("want only the two objects named ZCL_ORDER, got %+v", got)
	}
	if gotQuery != "zcl_order" || gotType != "CLAS/OC" || gotMax != "1000" {
		t.Fatalf("query=%q type=%q max=%q", gotQuery, gotType, gotMax)
	}

	got, err = c.SearchObjectExact(context.Background(), "ZCL_ORDER", "", 1)
	if err != nil || len(got) != 1 || got[0].Name != "ZCL_ORDER" {
		t.Fatalf("max 1: got %+v, %v", got, err)
	}

	if _, err := c.SearchObjectExact(context.Background(), "ZCL_*", "", 0); err == nil || !strings.Contains(err.Error(), "not a pattern") {
		t.Fatalf("a pattern must be refused, got %v", err)
	}
}

// The quick search matches a prefix and the exact search reads a window of
// 1000 hits. A window filled with longer names and no exact one cannot tell
// "absent" from "ranked beyond the window", and must say so.
func TestSearchObjectExactFullWindowIsInconclusive(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var sb strings.Builder
		sb.WriteString(`<?xml version="1.0" encoding="utf-8"?><adtcore:objectReferences xmlns:adtcore="http://www.sap.com/adt/core">`)
		for i := 0; i < hits; i++ {
			fmt.Fprintf(&sb, `<adtcore:objectReference adtcore:uri="/sap/bc/adt/oo/classes/zcl_x_%04d" adtcore:type="CLAS/OC" adtcore:name="ZCL_X_%04d"/>`, i, i)
		}
		sb.WriteString(`</adtcore:objectReferences>`)
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(sb.String()))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "u", "p")

	hits = exactSearchFetch
	_, err := c.SearchObjectExact(context.Background(), "ZCL_X", "", 0)
	if !errors.Is(err, ErrExactSearchWindowFull) ||
		!strings.Contains(err.Error(), "not found within the first 1000 prefix matches; pass type to narrow") {
		t.Fatalf("full window, no exact hit: got %v", err)
	}
	if _, err := c.SearchObjectExact(context.Background(), "ZCL_X", "CLAS", 0); err == nil || strings.Contains(err.Error(), "pass type") {
		t.Fatalf("with a type already given, the advice must not be to pass one: %v", err)
	}

	hits = exactSearchFetch - 1
	got, err := c.SearchObjectExact(context.Background(), "ZCL_X", "", 0)
	if err != nil || len(got) != 0 {
		t.Fatalf("a window that is not full is conclusive: got %+v, %v", got, err)
	}
}
