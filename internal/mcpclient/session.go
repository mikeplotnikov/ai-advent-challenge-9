package mcpclient

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// NewNamed builds a client that introduces itself to servers under the given name.
// Day 16's New keeps its own fixed name; an agent that calls tools is a different
// client and says so in clientInfo, which is what a server's logs will show.
func NewNamed(name, version string) *Client {
	return &Client{client: mcp.NewClient(&mcp.Implementation{Name: name, Version: version}, nil)}
}

// Session is an open MCP connection that can both list and call tools. Day 16 only
// ever needed List's connect-list-close in one call; an agent keeps the connection
// for the whole conversation, because the model may ask for several calls.
type Session struct {
	session   *mcp.ClientSession
	Transport Transport

	ServerName      string
	ServerVersion   string
	ProtocolVersion string
}

// ErrNilArguments guards the one thing CallTool must never do silently: send a
// call whose arguments the caller failed to parse.
var ErrNilArguments = errors.New("аргументы вызова не переданы")

// Open connects and completes initialization. The caller owns Close.
func (c *Client) Open(ctx context.Context, transport Transport) (*Session, error) {
	session, err := c.client.Connect(ctx, transport.MCP, nil)
	if err != nil {
		return nil, err
	}
	opened := &Session{session: session, Transport: transport}
	if initialized := session.InitializeResult(); initialized != nil {
		opened.ProtocolVersion = initialized.ProtocolVersion
		if initialized.ServerInfo != nil {
			opened.ServerName = initialized.ServerInfo.Name
			opened.ServerVersion = initialized.ServerInfo.Version
		}
	}
	return opened, nil
}

// ListTools asks the server what it offers.
func (s *Session) ListTools(ctx context.Context) (*mcp.ListToolsResult, error) {
	return s.session.ListTools(ctx, nil)
}

// CallTool runs one tool with arguments already decoded from the model's JSON.
// A tool that failed reports it in the result (IsError), not as a Go error: the Go
// error is kept for transport and protocol faults, which the model cannot fix.
func (s *Session) CallTool(ctx context.Context, name string, arguments map[string]any) (*mcp.CallToolResult, error) {
	if arguments == nil {
		return nil, ErrNilArguments
	}
	return s.session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
}

// Close ends the session. A child process is waited on so it does not outlive the
// CLI; an HTTP session is closed in the background for the reason client.go's List
// documents — the SDK may wait seconds for a DELETE the caller has no use for.
func (s *Session) Close() error {
	if s.Transport.Command != "" {
		return s.session.Close()
	}
	go func() { _ = s.session.Close() }()
	return nil
}

// ToolText joins the text content of a tool result. Structured content is carried
// by the SDK as a text copy too, so this is what a model receives as the result.
func ToolText(result *mcp.CallToolResult) string {
	if result == nil {
		return ""
	}
	text := ""
	for _, content := range result.Content {
		if part, ok := content.(*mcp.TextContent); ok {
			if text != "" {
				text += "\n"
			}
			text += part.Text
		}
	}
	if text == "" && result.StructuredContent != nil {
		if encoded, err := json.Marshal(result.StructuredContent); err == nil {
			text = string(encoded)
		}
	}
	return text
}
