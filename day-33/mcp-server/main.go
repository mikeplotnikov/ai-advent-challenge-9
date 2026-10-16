// Command mcp-server serves the day-33 support CRM (a JSON file) over MCP stdio.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/supportmcp"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	data := flag.String("data", filepath.Join("day-33", "data", "crm.json"), "JSON с пользователями, тикетами и инцидентами")
	flag.Parse()
	if _, err := supportmcp.Load(*data); err != nil {
		fmt.Fprintln(os.Stderr, "CRM:", err)
		os.Exit(1)
	}
	if err := supportmcp.NewServer(*data).Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		fmt.Fprintln(os.Stderr, "MCP stdio-сервер:", err)
		os.Exit(1)
	}
}
