package builtin

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"

	"github.com/hexagon-codes/hexclaw/skill"
)

func httpExecutionDetail(target, action string, response *http.Response) skill.ToolExecutionDetail {
	detail := skill.ToolExecutionDetail{Phase: "http", Outcome: "success", Action: action}
	if parsed, err := url.Parse(target); err == nil {
		detail.TargetHost = parsed.Hostname()
	}
	if response != nil {
		detail.ResponseReceived = true
		detail.HTTPStatus = response.StatusCode
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			detail.Outcome = "failure"
			detail.Reason = "http_status"
		}
	}
	return detail
}

func httpExecutionError(detail skill.ToolExecutionDetail, phase string, err error) *skill.ExecutionError {
	detail.Phase = phase
	detail.Outcome = "failure"
	detail.Reason = "unknown"
	var dns *net.DNSError
	var netErr net.Error
	switch {
	case errors.Is(err, context.Canceled):
		detail.Reason = "cancelled"
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		detail.Reason = "timeout"
	case errors.As(err, &dns):
		detail.Reason = "dns"
	}
	// POST 缺少确定响应时可能已经产生远端副作用，保留未知而不声称未执行。
	if detail.Action == "post" {
		detail.Outcome = "unknown"
	}
	return &skill.ExecutionError{Detail: detail, Err: fmt.Errorf("HTTP %s failed: %w", phase, err)}
}

func completeHTTPResult(result *skill.Result, detail skill.ToolExecutionDetail) (*skill.Result, error) {
	result.Detail = &detail
	if detail.Outcome != "success" {
		return result, &skill.ExecutionError{Detail: detail, Err: fmt.Errorf("HTTP status %d", detail.HTTPStatus)}
	}
	return result, nil
}

func partialHTTPResult(body []byte, detail skill.ToolExecutionDetail, readErr error) (*skill.Result, error) {
	err := httpExecutionError(detail, "read_response", readErr)
	return &skill.Result{Content: truncateRunes(stripHTML(string(body)), maxFetchTextLen), Detail: &err.Detail}, err
}
