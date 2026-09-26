package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/clockmcp"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	nowFlag := flag.String("now", "", "фиксированный момент RFC3339")
	flag.Parse()
	options := clockmcp.Options{}
	if *nowFlag != "" {
		value, err := time.Parse(time.RFC3339, *nowFlag)
		if err != nil {
			fmt.Fprintln(os.Stderr, "-now:", err)
			os.Exit(2)
		}
		options.Now = func() time.Time { return value }
	}
	if err := clockmcp.NewServer(options).Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
