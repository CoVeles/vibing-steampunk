package adt

import (
	"context"
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
