package adt

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// readOnlyMockClient is a client whose transport records every request; with
// readOnly set, it refuses writes.
func readOnlyMockClient(readOnly bool) (*Client, *mockTransportClient) {
	mock := &mockTransportClient{responses: map[string]*http.Response{}}
	cfg := NewConfig("https://sap.example.com:44300", "user", "pass")
	cfg.Safety.ReadOnly = readOnly
	return NewClientWithTransport(cfg, NewTransportWithClient(cfg, mock)), mock
}

// assertReadOnlyRefusal runs op on a read-only client and on a writable one:
// the first must be refused before any request, the second must send.
func assertReadOnlyRefusal(t *testing.T, op func(*Client) error) {
	t.Helper()
	c, mock := readOnlyMockClient(true)
	err := op(c)
	if err == nil || !strings.Contains(err.Error(), "blocked by safety configuration") {
		t.Fatalf("want a safety refusal, got %v", err)
	}
	if len(mock.requests) != 0 {
		t.Errorf("a refused operation sent %d request(s)", len(mock.requests))
	}
	c, mock = readOnlyMockClient(false)
	if err := op(c); err != nil && strings.Contains(err.Error(), "blocked") {
		t.Fatalf("refused without --read-only: %v", err)
	}
	if len(mock.requests) == 0 {
		t.Error("never reached SAP without --read-only")
	}
}

func TestServiceBindingPublish_RefusedUnderReadOnly(t *testing.T) {
	ctx := context.Background()
	t.Run("publish", func(t *testing.T) {
		assertReadOnlyRefusal(t, func(c *Client) error {
			_, err := c.PublishServiceBinding(ctx, "ZDEMO_SB", "0001")
			return err
		})
	})
	t.Run("unpublish", func(t *testing.T) {
		assertReadOnlyRefusal(t, func(c *Client) error {
			_, err := c.UnpublishServiceBinding(ctx, "ZDEMO_SB", "0001")
			return err
		})
	})
}
