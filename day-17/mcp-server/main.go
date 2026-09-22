package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/ratesmcp"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	var httpAddr string
	flag.StringVar(&httpAddr, "http", "", "адрес Streamable HTTP сервера")
	flag.Parse()
	server := ratesmcp.NewServer(ratesmcp.Options{})
	if httpAddr != "" {
		mux := http.NewServeMux()
		mux.Handle("/mcp", mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true}))
		if err := http.ListenAndServe(httpAddr, mux); err != nil {
			log.Printf("MCP HTTP-сервер: %v", err)
			os.Exit(1)
		}
		return
	}
	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		fmt.Fprintln(os.Stderr, "MCP stdio-сервер:", err)
		os.Exit(1)
	}
}
