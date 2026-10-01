package mcp

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/oisee/vibing-steampunk/pkg/adt"
)

// maxCallTimeout caps a per-call budget. An hour is far beyond any ABAP Unit
// run or deploy an agent should wait on synchronously.
const maxCallTimeout = time.Hour

// callTimeoutDescription documents the timeout parameter of every long call.
const callTimeoutDescription = "Seconds this call may take in all (default: the server's --call-timeout; without one, each request to SAP is limited to 60s). When it runs out the call returns a timeout message; the work may still be running on SAP."

// callBudget reads a call's budget: params.timeout in seconds, else the
// server default (--call-timeout / SAP_CALL_TIMEOUT). Zero means no budget of
// the call's own: each request to SAP is still bounded by the client's
// per-request timeout.
func callBudget(args map[string]any, serverDefault time.Duration) (time.Duration, error) {
	raw, ok := args["timeout"]
	if !ok || raw == nil {
		return serverDefault, nil
	}
	var secs float64
	switch v := raw.(type) {
	case float64:
		secs = v
	case int:
		secs = float64(v)
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return 0, fmt.Errorf("timeout must be a number of seconds, got %q", v)
		}
		secs = f
	default:
		return 0, fmt.Errorf("timeout must be a number of seconds, got %v", raw)
	}
	if math.IsNaN(secs) || secs <= 0 {
		return 0, fmt.Errorf("timeout must be a positive number of seconds, got %v", raw)
	}
	d := time.Duration(secs * float64(time.Second))
	if d > maxCallTimeout {
		d = maxCallTimeout
	}
	return d, nil
}

// longCall runs one long operation (ExecuteABAP, ABAP Unit, a deploy) under
// the call's budget, and turns the bare "context deadline exceeded",
// "context canceled" or "Client.Timeout exceeded" it would otherwise end with
// into a sentence that says what happened: the wait ended, the work on SAP may
// not have.
func (s *Server) longCall(ctx context.Context, request mcp.CallToolRequest, op string, h handlerFunc) (*mcp.CallToolResult, error) {
	var serverDefault time.Duration
	if s.config != nil {
		serverDefault = s.config.CallTimeout
	}
	budget, err := callBudget(request.GetArguments(), serverDefault)
	if err != nil {
		return newToolResultError(err.Error()), nil
	}
	callCtx := ctx
	if budget > 0 {
		var cancel context.CancelFunc
		callCtx, cancel = context.WithTimeout(ctx, budget)
		defer cancel()
		callCtx = adt.WithCallDeadline(callCtx)
	}
	start := time.Now()
	result, herr := h(callCtx, request)
	if msg := timeoutMessage(op, callCtx, budget, time.Since(start), result, herr); msg != "" {
		return newToolResultError(msg), nil
	}
	return result, herr
}

// timeoutMessage names a call that ended because a wait ran out, or "" when
// it did not. A call whose context ended is reported as such even when it
// produced a result: whatever it produced is incomplete, and is kept as detail.
func timeoutMessage(op string, ctx context.Context, budget, elapsed time.Duration, result *mcp.CallToolResult, herr error) string {
	// Not only an error result: ExecuteABAP reports a run it could not finish
	// as an ordinary result ("Success: false"), with the context's error
	// somewhere inside it.
	text := ""
	if herr != nil {
		text = herr.Error()
	} else {
		text = resultText(result)
	}
	const still = "the operation may still be running on SAP (check SM50/SM66 before retrying it)"
	var msg string
	switch {
	case budget > 0 && errors.Is(ctx.Err(), context.DeadlineExceeded):
		msg = fmt.Sprintf("%s timed out after %s; %s. Pass a larger params.timeout (seconds), or start the server with a larger --call-timeout.",
			op, seconds(budget), still)
	case errors.Is(ctx.Err(), context.Canceled):
		msg = fmt.Sprintf("%s was cancelled by the client after %s; %s.", op, seconds(elapsed), still)
	case strings.Contains(text, "Client.Timeout exceeded") || strings.Contains(text, "context deadline exceeded"):
		msg = fmt.Sprintf("%s timed out after %s: one request to SAP ran past the per-request limit; %s. Pass params.timeout (seconds) to give the whole call a longer budget.",
			op, seconds(elapsed), still)
	default:
		return ""
	}
	// What the handler said is kept: it can carry a cleanup warning (a
	// temporary program left behind, a lock not released) the caller needs.
	if detail := strings.TrimSpace(text); detail != "" {
		const max = 1000
		if len(detail) > max {
			detail = detail[:max] + " ..."
		}
		msg += "\nDetail: " + detail
	}
	return msg
}

func seconds(d time.Duration) string {
	if d < time.Second {
		return d.Round(time.Millisecond).String()
	}
	return fmt.Sprintf("%ds", int(math.Round(d.Seconds())))
}
