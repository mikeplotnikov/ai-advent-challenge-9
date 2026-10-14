// Command mcp-server serves read-only git facts about one repository over MCP stdio.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/gitmcp"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	repo := flag.String("repo", ".", "рабочая копия git, о которой отвечает сервер")
	flag.Parse()
	server := gitmcp.NewServer(gitmcp.Options{Repo: *repo})
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		fmt.Fprintln(os.Stderr, "MCP stdio-сервер:", err)
		os.Exit(1)
	}
}
