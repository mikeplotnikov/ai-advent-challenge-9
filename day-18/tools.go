package main

import (
	"context"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/mcpclient"
)

type ToolsServer struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	Protocol string `json:"protocol"`
}

type ToolsItem struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"inputSchema"`
}

type ToolsSnapshot struct {
	At     string      `json:"at"`
	Server ToolsServer `json:"server"`
	Tools  []ToolsItem `json:"tools"`
}

func writeTools(ctx context.Context, session *mcpclient.Session, path string, at time.Time) error {
	listed, err := session.ListTools(ctx)
	if err != nil {
		return err
	}
	snapshot := ToolsSnapshot{
		At: at.UTC().Format(time.RFC3339),
		Server: ToolsServer{
			Name:     session.ServerName,
			Version:  session.ServerVersion,
			Protocol: session.ProtocolVersion,
		},
		Tools: make([]ToolsItem, 0, len(listed.Tools)),
	}
	for _, tool := range listed.Tools {
		snapshot.Tools = append(snapshot.Tools, ToolsItem{Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema})
	}
	return writeJSONAtomic(path, snapshot)
}
