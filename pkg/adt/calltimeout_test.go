package adt

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A call given its own deadline must not be cut off by the client's shorter
// per-request Timeout; a call without one must still be.
func TestCallDeadlineLiftsClientTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("done"))
	}))
	defer srv.Close()

	cfg := NewConfig(srv.URL, "u", "p")
	tr := NewTransportWithClient(cfg, &http.Client{Timeout: 100 * time.Millisecond})

	if _, err := tr.Request(context.Background(), "/sap/bc/adt/slow", &RequestOptions{Method: http.MethodGet}); err == nil {
		t.Fatal("unmarked call: want the client Timeout to cut the request off")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := tr.Request(WithCallDeadline(ctx), "/sap/bc/adt/slow", &RequestOptions{Method: http.MethodGet})
	if err != nil {
		t.Fatalf("call with its own deadline: %v", err)
	}
	if string(resp.Body) != "done" {
		t.Fatalf("body = %q", resp.Body)
	}

	short, cancel2 := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel2()
	if _, err := tr.Request(WithCallDeadline(short), "/sap/bc/adt/slow", &RequestOptions{Method: http.MethodGet}); err == nil {
		t.Fatal("call deadline shorter than the request: want it to end the request")
	}
}

func TestWithCallDeadlineNeedsADeadline(t *testing.T) {
	if callDeadlineGoverns(WithCallDeadline(context.Background())) {
		t.Fatal("a context without a deadline must stay bounded by the client Timeout")
	}
}
