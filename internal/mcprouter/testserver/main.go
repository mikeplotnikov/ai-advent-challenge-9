package main

import (
	"context"
	"flag"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type probeOutput struct {
	PID      int               `json:"pid"`
	Argument string            `json:"argument"`
	Env      map[string]string `json:"env"`
}

func main() {
	argument := flag.String("arg", "", "probe argument")
	pidFile := flag.String("pid-file", "", "write pid")
	hangStart := flag.Bool("hang-start", false, "never initialize")
	ratesDie := flag.Bool("rates-die", false, "expose convert_currency and exit when called")
	flag.Parse()
	if *pidFile != "" {
		_ = os.WriteFile(*pidFile, []byte(strconv.Itoa(os.Getpid())), 0o600)
	}
	if *hangStart {
		time.Sleep(24 * time.Hour)
	}
	server := mcp.NewServer(&mcp.Implementation{Name: "router-test", Version: "1.0.0"}, nil)
	if *ratesDie {
		mcp.AddTool(server, &mcp.Tool{Name: "convert_currency", Description: "exit test server"},
			func(context.Context, *mcp.CallToolRequest, map[string]any) (*mcp.CallToolResult, map[string]any, error) {
				os.Exit(7)
				return nil, nil, nil
			})
		_ = server.Run(context.Background(), &mcp.StdioTransport{})
		return
	}
	mcp.AddTool(server, &mcp.Tool{Name: "probe", Description: "test probe"},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, probeOutput, error) {
			env := map[string]string{}
			for _, item := range os.Environ() {
				name, value, ok := strings.Cut(item, "=")
				if ok && (strings.HasPrefix(name, "DEEPSEEK_API_KEY") || name == "PROBE_VALUE") {
					env[name] = value
				}
			}
			return nil, probeOutput{PID: os.Getpid(), Argument: *argument, Env: env}, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "hang", Description: "hang forever"},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, map[string]any, error) {
			select {}
		})
	mcp.AddTool(server, &mcp.Tool{Name: "sleep", Description: "sleep briefly"},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, map[string]any, error) {
			select {
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			case <-time.After(time.Second):
				return nil, map[string]any{"ok": true}, nil
			}
		})
	_ = server.Run(context.Background(), &mcp.StdioTransport{})
}
