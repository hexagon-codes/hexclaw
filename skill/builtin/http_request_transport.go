package builtin

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/hexagon-codes/toolkit/net/httpx"
)

type httpTransportReplayError = httpx.TransportReplayError

func httpTransportReplayFacts(err error) map[string]any {
	var replay *httpx.TransportReplayError
	if !errors.As(err, &replay) {
		return nil
	}
	return map[string]any{
		"protocol": replay.Protocol, "replay_blocked": true, "evidence_source": "httptrace",
		"request_write_trace_started":  replay.RequestWriteTraceStarted,
		"request_write_trace_complete": replay.RequestWriteTraceComplete,
		"response_complete":            false,
	}
}

// observeBrowserHTTPTransport 只为 HTTP 工具选择下层可观察重放传输，不改变其它客户端默认。
func observeBrowserHTTPTransport(client *http.Client) error {
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		return fmt.Errorf("HTTP tool transport does not support observable attempts")
	}
	guard, err := httpx.NewReplayGuardTransport(transport)
	if err != nil {
		return err
	}
	client.Transport = guard
	return nil
}
