package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// An error from a running MCP server must not bring the CLI usage text with
// it; the MCP client shows stderr as the server's log.
func TestServeMCPErrorPrintsNoUsage(t *testing.T) {
	var stderr bytes.Buffer
	cmd := &cobra.Command{
		Use: "vsp",
		RunE: func(cmd *cobra.Command, args []string) error {
			return serveMCP(cmd, func() error { return errors.New("server stopped") })
		},
	}
	cmd.Flags().Bool("read-only", false, "a flag the usage text would list")
	cmd.SetOut(&stderr)
	cmd.SetErr(&stderr)
	cmd.SetArgs(nil)

	if err := cmd.Execute(); err == nil {
		t.Fatal("want the server's error back")
	}
	if strings.Contains(stderr.String(), "Usage:") || strings.Contains(stderr.String(), "--read-only") {
		t.Fatalf("usage printed for a server error:\n%s", stderr.String())
	}
}

func TestResolveCallTimeout(t *testing.T) {
	t.Setenv("SAP_CALL_TIMEOUT", "")
	cmd := &cobra.Command{Use: "vsp"}
	cmd.Flags().Int("call-timeout", 0, "")
	if got := resolveCallTimeout(cmd); got != 0 {
		t.Fatalf("no flag, no env: got %v, want 0", got)
	}
	t.Setenv("SAP_CALL_TIMEOUT", "300")
	if got := resolveCallTimeout(cmd); got.Seconds() != 300 {
		t.Fatalf("SAP_CALL_TIMEOUT=300: got %v", got)
	}
	_ = cmd.Flags().Set("call-timeout", "90")
	if got := resolveCallTimeout(cmd); got.Seconds() != 90 {
		t.Fatalf("flag 90 over env: got %v", got)
	}
}
