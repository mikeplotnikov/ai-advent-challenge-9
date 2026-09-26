package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/mcprouter"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/toolagent"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type firstTurnLLM struct{ inner toolagent.LLM }

func (f firstTurnLLM) Converse(ctx context.Context, messages []llm.ToolMessage,
	options llm.Options) (llm.Answer, error) {
	answer, err := f.inner.Converse(ctx, messages, options)
	answer.ToolCalls = nil
	return answer, err
}

type filteredSession struct {
	router   *mcprouter.Router
	prefixes []string
}

func (f filteredSession) ListTools(ctx context.Context) (*mcp.ListToolsResult, error) {
	listed, err := f.router.ListTools(ctx)
	if err != nil {
		return nil, err
	}
	result := &mcp.ListToolsResult{}
	for _, tool := range listed.Tools {
		for _, prefix := range f.prefixes {
			if strings.HasPrefix(tool.Name, prefix+"__") {
				result.Tools = append(result.Tools, tool)
			}
		}
	}
	return result, nil
}

func (f filteredSession) CallTool(ctx context.Context, name string,
	arguments map[string]any) (*mcp.CallToolResult, error) {
	return f.router.CallTool(ctx, name, arguments)
}

type SchemaCostVariant struct {
	Name         string      `json:"name"`
	SchemaBytes  int         `json:"schemaBytes"`
	PromptTokens []int       `json:"promptTokens"`
	Delta        []int       `json:"deltaVsNoTools"`
	Usage        []llm.Usage `json:"usage"`
}

type SchemaCostReport struct {
	At       string              `json:"at"`
	Question string              `json:"question"`
	Variants []SchemaCostVariant `json:"variants"`
	Cost     float64             `json:"cost"`
	Known    bool                `json:"costKnown"`
}

func runSchemaCost(registry mcprouter.Registry, timeout time.Duration, stdout, stderr io.Writer) int {
	llm.LoadDotEnv(".env")
	model, err := llm.NewWithKeyEnv("DEEPSEEK_API_KEY_DAY20")
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	model.Model = modelName
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	router, err := mcprouter.Open(ctx, registryForBench(registry, os.TempDir()), mcprouter.Options{})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer router.Close()
	report, err := measureSchemaCost(ctx, router, model)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := writeSchemaCostSection("day-20/RESULTS.md", report); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	for _, variant := range report.Variants {
		fmt.Fprintf(stdout, "%s: prompt_tokens=%v delta=%v schema=%d B\n",
			variant.Name, variant.PromptTokens, variant.Delta, variant.SchemaBytes)
	}
	return 0
}

func measureSchemaCost(ctx context.Context, router *mcprouter.Router,
	model toolagent.LLM) (SchemaCostReport, error) {
	variants := []struct {
		name     string
		prefixes []string
		noTools  bool
	}{
		{"no-tools", nil, true}, {"clock", []string{"clock"}, false},
		{"rates", []string{"rates"}, false}, {"pipeline", []string{"pipeline"}, false},
		{"all", []string{"clock", "rates", "pipeline"}, false},
	}
	report := SchemaCostReport{At: time.Now().Format(time.RFC3339), Question: defaultQuestion, Known: true}
	for _, variant := range variants {
		session := filteredSession{router: router, prefixes: variant.prefixes}
		listed, _ := session.ListTools(ctx)
		raw, _ := json.Marshal(listed.Tools)
		entry := SchemaCostVariant{Name: variant.name, SchemaBytes: len(raw)}
		if variant.noTools {
			entry.SchemaBytes = 0
		}
		for repeat := 0; repeat < 3; repeat++ {
			trace, err := toolagent.Run(ctx, firstTurnLLM{model}, session, toolagent.Input{
				SystemPrompt: systemPrompt, Question: defaultQuestion, NoTools: variant.noTools,
			})
			if err != nil {
				return report, err
			}
			if len(trace.ModelCalls) != 1 {
				return report, fmt.Errorf("%s: первый ход сделал %d обращений", variant.name, len(trace.ModelCalls))
			}
			entry.PromptTokens = append(entry.PromptTokens, trace.ModelCalls[0].Usage.PromptTokens)
			entry.Usage = append(entry.Usage, trace.ModelCalls[0].Usage)
			report.Cost += trace.Totals.Cost
			report.Known = report.Known && trace.Totals.CostKnown
		}
		report.Variants = append(report.Variants, entry)
	}
	base := report.Variants[0].PromptTokens
	for i := range report.Variants {
		for repeat := 0; repeat < 3; repeat++ {
			report.Variants[i].Delta = append(report.Variants[i].Delta,
				report.Variants[i].PromptTokens[repeat]-base[repeat])
		}
	}
	return report, nil
}

func writeSchemaCostSection(path string, report SchemaCostReport) error {
	var section strings.Builder
	section.WriteString("<!-- schema-cost:start -->\n## Цена схем\n\n")
	section.WriteString("| Набор | Байты JSON | prompt_tokens ×3 | Δ к без инструментов |\n|---|---:|---:|---:|\n")
	for _, variant := range report.Variants {
		fmt.Fprintf(&section, "| %s | %d | %v | %v |\n", variant.Name, variant.SchemaBytes,
			variant.PromptTokens, variant.Delta)
	}
	section.WriteString("<!-- schema-cost:end -->")
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	text := string(raw)
	if strings.Contains(text, "<!-- schema-cost:start -->") {
		text = replaceBetween(text, "<!-- schema-cost:start -->", "<!-- schema-cost:end -->", section.String())
	} else {
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		text += "\n" + section.String() + "\n"
	}
	return os.WriteFile(path, []byte(text), 0o644)
}
