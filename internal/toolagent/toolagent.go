// Package toolagent runs the model-tool loop shared by the MCP challenge days.
package toolagent

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const MaxModelCalls = 5

// LLM is deliberately small so the loop is testable without a provider.
type LLM interface {
	Converse(context.Context, []llm.ToolMessage, llm.Options) (llm.Answer, error)
}

// Session is the part of an MCP session the agent needs.
type Session interface {
	ListTools(context.Context) (*mcp.ListToolsResult, error)
	CallTool(context.Context, string, map[string]any) (*mcp.CallToolResult, error)
}

type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}
type ModelCall struct {
	At           time.Time `json:"at"`
	RequestBody  string    `json:"requestBody"`
	ResponseBody string    `json:"responseBody"`
	FinishReason string    `json:"finishReason"`
	Model        string    `json:"model"`
	Usage        llm.Usage `json:"usage"`
	Cost         float64   `json:"cost,omitempty"`
	CostKnown    bool      `json:"costKnown"`
}
type ToolCall struct {
	// Step is the one-based model call which requested this tool invocation.
	Step         int    `json:"step"`
	ID           string `json:"id"`
	Name         string `json:"name"`
	RawArguments string `json:"rawArguments"`
	ParseError   string `json:"parseError,omitempty"`
	// Rejected is non-empty when the agent did not send a tools/call request.
	Rejected   string         `json:"rejected,omitempty"`
	Arguments  map[string]any `json:"arguments,omitempty"`
	IsError    bool           `json:"isError"`
	ResultText string         `json:"resultText,omitempty"`
	Structured any            `json:"structured,omitempty"`
}
type Totals struct {
	ModelCalls   int           `json:"modelCalls"`
	ToolCalls    int           `json:"toolCalls"`
	PromptTokens int           `json:"promptTokens"`
	CachedTokens int           `json:"cachedTokens"`
	OutputTokens int           `json:"outputTokens"`
	Cost         float64       `json:"cost,omitempty"`
	CostKnown    bool          `json:"costKnown"`
	Duration     time.Duration `json:"duration"`
}
type Trace struct {
	Server      Server      `json:"server"`
	Tools       []Tool      `json:"tools,omitempty"`
	ModelCalls  []ModelCall `json:"modelCalls"`
	ToolCalls   []ToolCall  `json:"toolCalls"`
	FinalAnswer string      `json:"finalAnswer,omitempty"`
	Totals      Totals      `json:"totals"`
}

// Server identifies the MCP peer which supplied the schemas and tool results.
type Server struct {
	Name      string `json:"name,omitempty"`
	Version   string `json:"version,omitempty"`
	Protocol  string `json:"protocol,omitempty"`
	Transport string `json:"transport,omitempty"`
}
type Input struct {
	SystemPrompt, Question string
	NoTools                bool
}

// Run lists the server tools once, then lets the provider choose calls until it writes text.
func Run(ctx context.Context, model LLM, session Session, in Input) (Trace, error) {
	started := time.Now()
	trace := Trace{Totals: Totals{CostKnown: true}}
	var tools []llm.Tool
	known := map[string]bool{}
	if !in.NoTools {
		listed, err := session.ListTools(ctx)
		if err != nil {
			return trace, err
		}
		for _, tool := range listed.Tools {
			schema, err := json.Marshal(tool.InputSchema)
			if err != nil {
				return trace, fmt.Errorf("схема инструмента %q: %w", tool.Name, err)
			}
			trace.Tools = append(trace.Tools, Tool{Name: tool.Name, Description: tool.Description, InputSchema: schema})
			tools = append(tools, llm.Tool{Type: "function", Function: llm.ToolFunction{Name: tool.Name, Description: tool.Description, Parameters: schema}})
			known[tool.Name] = true
		}
	}
	messages := []llm.ToolMessage{{Role: "system", Content: in.SystemPrompt}, {Role: "user", Content: in.Question}}
	zero := 0.0
	for step := 0; step < MaxModelCalls; step++ {
		at := time.Now()
		opts := llm.Options{Temperature: &zero}
		if !in.NoTools {
			opts.Tools = tools
		}
		answer, err := model.Converse(ctx, messages, opts)
		if err != nil {
			trace.Totals.Duration = time.Since(started)
			return trace, err
		}
		cost, knownCost := llm.CostAt(answer.Model, answer.Usage, at)
		trace.ModelCalls = append(trace.ModelCalls, ModelCall{At: at, RequestBody: answer.RequestBody, ResponseBody: answer.ResponseBody, FinishReason: answer.FinishReason, Model: answer.Model, Usage: answer.Usage, Cost: cost, CostKnown: knownCost})
		trace.Totals.ModelCalls++
		trace.Totals.PromptTokens += answer.Usage.PromptTokens
		trace.Totals.CachedTokens += answer.Usage.PromptCacheHitTokens
		trace.Totals.OutputTokens += answer.Usage.CompletionTokens
		if !knownCost {
			trace.Totals.CostKnown = false
		} else {
			trace.Totals.Cost += cost
		}
		if len(answer.ToolCalls) == 0 {
			trace.FinalAnswer = answer.Content
			trace.Totals.Duration = time.Since(started)
			return trace, nil
		}
		messages = append(messages, llm.ToolMessage{Role: "assistant", Content: "", ToolCalls: answer.ToolCalls})
		for _, call := range answer.ToolCalls {
			record := ToolCall{Step: step + 1, ID: call.ID, Name: call.Function.Name, RawArguments: call.Function.Arguments}
			content := ""
			if !known[call.Function.Name] {
				names := make([]string, 0, len(known))
				for name := range known {
					names = append(names, name)
				}
				sort.Strings(names)
				content = fmt.Sprintf("инструмент %q не существует; доступны: %s", call.Function.Name, strings.Join(names, ", "))
				record.Rejected = content
			} else {
				var args map[string]any
				if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil || args == nil {
					reason := "ожидался JSON-объект"
					if err != nil {
						reason = err.Error()
					}
					record.ParseError = reason
					content = "аргументы не разобраны как JSON: " + reason
					record.Rejected = content
				} else {
					record.Arguments = args
					result, err := session.CallTool(ctx, call.Function.Name, args)
					if err != nil {
						trace.Totals.Duration = time.Since(started)
						return trace, err
					}
					record.IsError = result.IsError
					record.ResultText = mcpclient.ToolText(result)
					record.Structured = result.StructuredContent
					content = record.ResultText
					if record.IsError {
						content = "ОШИБКА ИНСТРУМЕНТА: " + content
					}
				}
			}
			trace.ToolCalls = append(trace.ToolCalls, record)
			trace.Totals.ToolCalls++
			messages = append(messages, llm.ToolMessage{Role: "tool", Content: content, ToolCallID: call.ID})
		}
	}
	trace.Totals.Duration = time.Since(started)
	return trace, fmt.Errorf("модель не дала ответа за %d обращений", MaxModelCalls)
}
