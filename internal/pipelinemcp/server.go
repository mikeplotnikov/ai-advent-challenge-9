package pipelinemcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/cbr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Options struct {
	CBR        cbr.Options
	ReportsDir string
}

type fetchInput struct {
	Currency string `json:"currency" jsonschema:"Буквенный код валюты ЦБ, например USD, EUR или CNY."`
	DateFrom string `json:"date_from" jsonschema:"Начало периода включительно в формате ГГГГ-ММ-ДД."`
	DateTo   string `json:"date_to" jsonschema:"Конец периода включительно в формате ГГГГ-ММ-ДД; период не длиннее 93 дней."`
}

type summarizeInput struct {
	DatasetID string `json:"dataset_id" jsonschema:"Идентификатор ds_…, дословно возвращённый fetch_rates."`
}

type saveInput struct {
	SummaryID string `json:"summary_id" jsonschema:"Идентификатор sm_…, дословно возвращённый summarize_rates."`
}

type FetchOutput struct {
	Count         int        `json:"count"`
	Currency      string     `json:"currency"`
	Dataset       Dataset    `json:"dataset"`
	DatasetID     string     `json:"dataset_id"`
	DatasetSHA256 string     `json:"dataset_sha256"`
	DateFrom      string     `json:"date_from"`
	DateTo        string     `json:"date_to"`
	First         DatasetRow `json:"first"`
	Last          DatasetRow `json:"last"`
	Name          string     `json:"name"`
}

type SummarizeOutput struct {
	DatasetSHA256 string  `json:"dataset_sha256"`
	Summary       Summary `json:"summary"`
	SummaryID     string  `json:"summary_id"`
	SummarySHA256 string  `json:"summary_sha256"`
}

type HashChain struct {
	DatasetSHA256 string `json:"dataset_sha256"`
	ReportSHA256  string `json:"report_sha256"`
	SummarySHA256 string `json:"summary_sha256"`
}

type SaveOutput struct {
	Bytes        int       `json:"bytes"`
	Chain        HashChain `json:"chain"`
	Name         string    `json:"name"`
	Path         string    `json:"path"`
	ReportSHA256 string    `json:"report_sha256"`
}

type storedDataset struct {
	value Dataset
	hash  string
}

type storedSummary struct {
	value     Summary
	hash      string
	datasetID string
}

type state struct {
	mu        sync.RWMutex
	datasets  map[string]storedDataset
	summaries map[string]storedSummary
}

func NewServer(options Options) *mcp.Server {
	client := cbr.New(options.CBR)
	memory := &state{datasets: make(map[string]storedDataset), summaries: make(map[string]storedSummary)}
	server := mcp.NewServer(&mcp.Implementation{Name: ServerName, Version: ServerVersion}, nil)

	mcp.AddTool(server, &mcp.Tool{Name: "fetch_rates", Description: "Получить официальные публикации ЦБ одной валюты за период и сохранить датасет; первым шагом вызовите этот инструмент, затем передайте dataset_id в summarize_rates."}, func(ctx context.Context, _ *mcp.CallToolRequest, input fetchInput) (*mcp.CallToolResult, FetchOutput, error) {
		rates, err := client.GetRange(ctx, input.Currency, input.DateFrom, input.DateTo)
		if err != nil {
			return nil, FetchOutput{}, toolError(err)
		}
		dataset := DatasetFromRange(rates)
		_, hash, err := HashCanonical(dataset)
		if err != nil {
			return nil, FetchOutput{}, err
		}
		id := shortID("ds_", hash)
		memory.mu.Lock()
		memory.datasets[id] = storedDataset{value: dataset, hash: hash}
		memory.mu.Unlock()
		output := FetchOutput{DatasetID: id, DatasetSHA256: hash, Currency: dataset.Currency, Name: dataset.Name, DateFrom: dataset.DateFrom, DateTo: dataset.DateTo, Count: len(dataset.Rows), First: dataset.Rows[0], Last: dataset.Rows[len(dataset.Rows)-1], Dataset: dataset}
		text := fmt.Sprintf("dataset_id: %s\ndataset_sha256: %s\ncurrency: %s\nname: %s\nperiod: %s—%s\ncount: %d\nfirst: %s %s\nlast: %s %s", id, hash, dataset.Currency, dataset.Name, dataset.DateFrom, dataset.DateTo, len(dataset.Rows), output.First.Date, output.First.UnitRate, output.Last.Date, output.Last.UnitRate)
		return textResult(text), output, nil
	})

	mcp.AddTool(server, &mcp.Tool{Name: "summarize_rates", Description: "Посчитать детерминированную сводку сохранённого датасета; передайте dataset_id дословно из fetch_rates, затем передайте summary_id в save_report."}, func(_ context.Context, _ *mcp.CallToolRequest, input summarizeInput) (*mcp.CallToolResult, SummarizeOutput, error) {
		id := strings.TrimSpace(input.DatasetID)
		if strings.HasPrefix(id, "sm_") {
			return nil, SummarizeOutput{}, errors.New("получен summary_id вместо dataset_id; сначала вызовите fetch_rates и передайте его dataset_id")
		}
		memory.mu.RLock()
		dataset, ok := memory.datasets[id]
		memory.mu.RUnlock()
		if !ok {
			return nil, SummarizeOutput{}, fmt.Errorf("dataset_id %q неизвестен; сначала вызовите fetch_rates и передайте его dataset_id", id)
		}
		summary, err := Summarize(dataset.value, dataset.hash)
		if err != nil {
			return nil, SummarizeOutput{}, err
		}
		_, hash, err := HashCanonical(summary)
		if err != nil {
			return nil, SummarizeOutput{}, err
		}
		summaryID := shortID("sm_", hash)
		memory.mu.Lock()
		memory.summaries[summaryID] = storedSummary{value: summary, hash: hash, datasetID: id}
		memory.mu.Unlock()
		output := SummarizeOutput{DatasetSHA256: dataset.hash, SummaryID: summaryID, SummarySHA256: hash, Summary: summary}
		text := fmt.Sprintf("summary_id: %s\nsummary_sha256: %s\ndataset_sha256: %s\ncount: %s\nfirst: %s %s\nlast: %s %s\nmin: %s %s\nmax: %s %s\nmean: %s\nchange_abs: %s\nchange_pct: %s", summaryID, hash, dataset.hash, summary.Count, summary.First.Date, summary.First.UnitRate, summary.Last.Date, summary.Last.UnitRate, summary.Min.Date, summary.Min.UnitRate, summary.Max.Date, summary.Max.UnitRate, summary.Mean, summary.ChangeAbs, summary.ChangePct)
		return textResult(text), output, nil
	})

	mcp.AddTool(server, &mcp.Tool{Name: "save_report", Description: "Сохранить проверяемый Markdown-отчёт по готовой сводке; передайте summary_id дословно из summarize_rates."}, func(_ context.Context, _ *mcp.CallToolRequest, input saveInput) (*mcp.CallToolResult, SaveOutput, error) {
		id := strings.TrimSpace(input.SummaryID)
		if strings.HasPrefix(id, "ds_") {
			return nil, SaveOutput{}, errors.New("получен dataset_id вместо summary_id; сначала вызовите summarize_rates и передайте его summary_id")
		}
		memory.mu.RLock()
		summary, ok := memory.summaries[id]
		dataset := memory.datasets[summary.datasetID]
		memory.mu.RUnlock()
		if !ok {
			return nil, SaveOutput{}, fmt.Errorf("summary_id %q неизвестен; сначала вызовите summarize_rates и передайте его summary_id", id)
		}
		raw, err := RenderReport(dataset.value, dataset.hash, summary.value, summary.hash)
		if err != nil {
			return nil, SaveOutput{}, err
		}
		name := ReportName(dataset.value, summary.hash)
		path, err := writeReportAtomic(options.ReportsDir, name, raw)
		if err != nil {
			return nil, SaveOutput{}, err
		}
		reportHash := reportSHA(raw)
		output := SaveOutput{Bytes: len(raw), Name: name, Path: path, ReportSHA256: reportHash, Chain: HashChain{DatasetSHA256: dataset.hash, SummarySHA256: summary.hash, ReportSHA256: reportHash}}
		text := fmt.Sprintf("name: %s\npath: %s\nbytes: %d\nreport_sha256: %s\ndataset_sha256: %s\nsummary_sha256: %s", name, path, len(raw), reportHash, dataset.hash, summary.hash)
		return textResult(text), output, nil
	})
	return server
}

func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

func toolError(err error) error {
	var source *cbr.SourceError
	if errors.As(err, &source) {
		return fmt.Errorf("ЦБ недоступен: %w", source.Err)
	}
	return err
}
