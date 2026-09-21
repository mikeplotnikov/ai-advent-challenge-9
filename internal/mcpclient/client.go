package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Client is a reusable MCP tools/list client.
type Client struct{ client *mcp.Client }

const DefaultTimeout = 30 * time.Second

// New constructs the client implementation used for every transport.
func New() (*Client, error) {
	return &Client{client: mcp.NewClient(&mcp.Implementation{Name: "ai-advent-day-16", Version: "1"}, nil)}, nil
}

// Result contains the negotiated metadata, tools/list result and criterion verdict.
type Result struct {
	Tools           *mcp.ListToolsResult
	ServerName      string
	ServerVersion   string
	ProtocolVersion string
	Transport       Transport
	Verdict         Verdict
	ResponseBytes   int
}

// List connects, initializes, lists tools and validates the response.
func (c *Client) List(ctx context.Context, transport Transport) (Result, error) {
	session, err := c.client.Connect(ctx, transport.MCP, nil)
	if err != nil {
		return Result{}, err
	}
	// StreamableClientTransport.Close may wait for a stateful server's DELETE response
	// for up to the SDK's five-second cleanup timeout. Cleanup must not extend the
	// caller's connect+initialize+tools/list deadline, so HTTP cleanup is not awaited.
	// CommandTransport owns a child process, however, and must be waited on so its
	// shutdown and Wait complete before a CLI's os.Exit can end the process.
	defer func() {
		if transport.Command != "" {
			_ = session.Close()
			return
		}
		go func() { _ = session.Close() }()
	}()
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		return Result{}, err
	}
	verdict, err := EvaluateToolsResult(tools)
	if err != nil {
		return Result{}, err
	}
	if transport.Capture != nil {
		exchanges := transport.Capture.Exchanges()
		if captured, ok, err := EvaluateCapturedToolsList(exchanges); err != nil {
			return Result{}, err
		} else if ok {
			verdict = captured
		}
	}
	if !verdict.Passed {
		return Result{}, fmt.Errorf("ответ tools/list не проходит критерий: %v", verdict.FailedRules())
	}
	result := Result{Tools: tools, Transport: transport, Verdict: verdict}
	if transport.Capture != nil {
		result.ResponseBytes = capturedResponseBytes(transport.Capture.Exchanges())
	}
	if initialized := session.InitializeResult(); initialized != nil {
		result.ProtocolVersion = initialized.ProtocolVersion
		if initialized.ServerInfo != nil {
			result.ServerName = initialized.ServerInfo.Name
			result.ServerVersion = initialized.ServerInfo.Version
		}
	}
	return result, nil
}

func capturedResponseBytes(exchanges []Exchange) int {
	for _, exchange := range exchanges {
		var request map[string]any
		if json.Unmarshal(exchange.RequestBody, &request) == nil && request["method"] == "tools/list" {
			return len(exchange.ResponseBody)
		}
	}
	return 0
}

// ParseTimeout accepts the CLI duration syntax while keeping its contract reusable.
func ParseTimeout(value string) (time.Duration, error) {
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("некорректный -timeout: %q", value)
	}
	if duration <= 0 {
		return 0, fmt.Errorf("-timeout должен быть больше нуля")
	}
	return duration, nil
}

// EvaluateCapturedToolsList evaluates the actual request/response envelope when an
// HTTP exchange is available. The SDK-decoded result still supplies the stdio path.
func EvaluateCapturedToolsList(exchanges []Exchange) (Verdict, bool, error) {
	for _, exchange := range exchanges {
		var request map[string]any
		if json.Unmarshal(exchange.RequestBody, &request) != nil || request["method"] != "tools/list" {
			continue
		}
		var response map[string]any
		if err := json.Unmarshal(exchange.ResponseBody, &response); err != nil {
			return Verdict{}, true, fmt.Errorf("не удалось разобрать raw-ответ tools/list: %w", err)
		}
		return Evaluate(request, response), true, nil
	}
	return Verdict{}, false, nil
}
