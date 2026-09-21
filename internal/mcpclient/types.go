// Package mcpclient provides a small reusable client for MCP tools/list calls.
package mcpclient

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const DefaultEndpoint = "https://mcp.deepwiki.com/mcp"

var ErrTransportChoice = errors.New("укажи либо -endpoint, либо -command")

// Transport describes the selected MCP transport without exposing day-specific CLI flags.
type Transport struct {
	MCP         mcp.Transport
	Description string
	Endpoint    string
	Command     string
	Capture     *Capture
}

// NewTransport applies the endpoint/command selection table used by the CLI.
func NewTransport(endpoint, command string) (Transport, error) {
	switch {
	case endpoint != "" && command != "":
		return Transport{}, ErrTransportChoice
	case command != "":
		parts := strings.Fields(command)
		if len(parts) == 0 {
			return Transport{}, fmt.Errorf("пустая команда MCP-сервера")
		}
		return Transport{
			MCP:         &mcp.CommandTransport{Command: exec.Command(parts[0], parts[1:]...)},
			Description: "stdio",
			Command:     command,
		}, nil
	default:
		if endpoint == "" {
			endpoint = DefaultEndpoint
		}
		capture := NewCapture()
		client := &httpClientForCapture{capture: capture}
		return Transport{
			MCP: &mcp.StreamableClientTransport{
				Endpoint:             endpoint,
				HTTPClient:           client.Client(),
				DisableStandaloneSSE: true,
			},
			Description: "Streamable HTTP",
			Endpoint:    endpoint,
			Capture:     capture,
		}, nil
	}
}
