package main

import (
	"encoding/json"
	"io"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/mcpclient"
)

type dumpRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
}

type dumpTool struct {
	Name        string `json:"name"`
	InputSchema any    `json:"inputSchema"`
}

type dumpResult struct {
	Tools []dumpTool `json:"tools"`
}

type dumpResponse struct {
	JSONRPC string     `json:"jsonrpc"`
	ID      int        `json:"id"`
	Result  dumpResult `json:"result"`
}

type dumpCase struct {
	Name     string            `json:"name"`
	Request  dumpRequest       `json:"request"`
	Response dumpResponse      `json:"response"`
	Verdict  mcpclient.Verdict `json:"verdict"`
}

type definitionsDump struct {
	Rules []mcpclient.Rule `json:"rules"`
	Cases []dumpCase       `json:"cases"`
}

func writeDump(w io.Writer) error {
	request := dumpRequest{JSONRPC: "2.0", ID: 16, Method: "tools/list"}
	valid := dumpResponse{JSONRPC: "2.0", ID: 16, Result: dumpResult{Tools: []dumpTool{{Name: "search", InputSchema: map[string]any{"type": "object"}}}}}
	cases := []struct {
		name     string
		response dumpResponse
	}{
		{"valid", valid},
		{"invalid-r1", dumpResponse{JSONRPC: "1.0", ID: 16, Result: valid.Result}},
		{"invalid-r2", dumpResponse{JSONRPC: "2.0", ID: 17, Result: valid.Result}},
		{"invalid-r3", dumpResponse{JSONRPC: "2.0", ID: 16, Result: dumpResult{Tools: []dumpTool{}}}},
		{"invalid-r4", dumpResponse{JSONRPC: "2.0", ID: 16, Result: dumpResult{Tools: []dumpTool{{Name: "", InputSchema: map[string]any{"type": "object"}}}}}},
		{"invalid-r5", dumpResponse{JSONRPC: "2.0", ID: 16, Result: dumpResult{Tools: []dumpTool{{Name: "search", InputSchema: "not-an-object"}}}}},
	}
	dump := definitionsDump{Rules: append([]mcpclient.Rule(nil), mcpclient.Rules...)}
	for _, fixture := range cases {
		requestObject := decodeObject(request)
		responseObject := decodeObject(fixture.response)
		dump.Cases = append(dump.Cases, dumpCase{Name: fixture.name, Request: request, Response: fixture.response, Verdict: mcpclient.Evaluate(requestObject, responseObject)})
	}
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(dump)
}

func decodeObject(value any) map[string]any {
	encoded, _ := json.Marshal(value)
	var object map[string]any
	_ = json.Unmarshal(encoded, &object)
	return object
}
