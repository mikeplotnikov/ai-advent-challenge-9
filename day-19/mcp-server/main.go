package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/pipelinemcp"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	defaultReports := os.Getenv("DAY19_REPORTS_DIR")
	if defaultReports == "" {
		defaultReports = "day-19/reports"
	}
	reports := flag.String("reports", defaultReports, "папка для отчётов")
	flag.Parse()
	server := pipelinemcp.NewServer(pipelinemcp.Options{ReportsDir: *reports})
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		fmt.Fprintln(os.Stderr, "MCP stdio-сервер:", err)
		os.Exit(1)
	}
}
