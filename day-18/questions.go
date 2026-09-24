package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/toolagent"
)

const (
	maxQuestions       = 100
	maxToolResultRunes = 4000
	truncatedMarker    = "…(обрезано)"
)

type QuestionToolCall struct {
	Step      int            `json:"step"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
	Result    string         `json:"result"`
	IsError   bool           `json:"is_error"`
	Rejected  string         `json:"rejected"`
}

type QuestionRecord struct {
	RequestID  string             `json:"request_id"`
	At         string             `json:"at"`
	Question   string             `json:"question"`
	Answer     string             `json:"answer"`
	Error      string             `json:"error"`
	Model      string             `json:"model"`
	ModelCalls int                `json:"model_calls"`
	ToolCalls  []QuestionToolCall `json:"tool_calls"`
	Tokens     DigestTokens       `json:"tokens"`
	Cost       float64            `json:"cost"`
	CostKnown  bool               `json:"cost_known"`
}

func questionFromTrace(requestID string, at time.Time, question string, trace toolagent.Trace, runErr error) QuestionRecord {
	record := QuestionRecord{
		RequestID: requestID,
		At:        at.UTC().Format(time.RFC3339),
		Question:  question,
		Answer:    trace.FinalAnswer,
		ToolCalls: []QuestionToolCall{},
		Tokens: DigestTokens{
			Prompt:  trace.Totals.PromptTokens,
			Cached:  trace.Totals.CachedTokens,
			Output:  trace.Totals.OutputTokens,
			PerCall: []TokenCall{},
		},
		Cost:      trace.Totals.Cost,
		CostKnown: trace.Totals.CostKnown,
	}
	if runErr != nil {
		record.Answer = ""
		record.Error = publicQuestionError(runErr)
	}
	for _, call := range trace.ModelCalls {
		record.Model = call.Model
		record.Tokens.PerCall = append(record.Tokens.PerCall, TokenCall{call.Usage.PromptTokens, call.Usage.PromptCacheHitTokens, call.Usage.CompletionTokens})
	}
	record.ModelCalls = trace.Totals.ModelCalls
	for _, call := range trace.ToolCalls {
		result := call.ResultText
		if call.Rejected != "" {
			result = ""
		}
		record.ToolCalls = append(record.ToolCalls, QuestionToolCall{
			Step:      call.Step,
			Name:      call.Name,
			Arguments: call.Arguments,
			Result:    truncateToolResult(result),
			IsError:   call.IsError,
			Rejected:  call.Rejected,
		})
	}
	return record
}

func publicQuestionError(err error) string {
	text := err.Error()
	if strings.HasPrefix(text, "API вернул ") {
		if body := strings.Index(text, ": "); body >= 0 {
			return text[:body]
		}
	}
	return text
}

func truncateToolResult(value string) string {
	if utf8.RuneCountInString(value) <= maxToolResultRunes {
		return value
	}
	limit := maxToolResultRunes - utf8.RuneCountInString(truncatedMarker)
	runes := []rune(value)
	return string(runes[:limit]) + truncatedMarker
}

func appendQuestion(path string, record QuestionRecord) error {
	busy, err := withDigestLock(path, func() error {
		records, err := readQuestions(path)
		if err != nil {
			return err
		}
		if len(records) >= maxQuestions {
			records = append([]QuestionRecord(nil), records[len(records)-maxQuestions+1:]...)
		}
		records = append(records, record)
		return writeJSONAtomic(path, records)
	})
	if busy {
		return errors.New("другой процесс уже записывает вопрос")
	}
	return err
}

func readQuestions(path string) ([]QuestionRecord, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return []QuestionRecord{}, nil
	}
	if err != nil {
		return nil, err
	}
	var records []QuestionRecord
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&records); err != nil || records == nil {
		if err == nil {
			err = errors.New("ожидался JSON-массив")
		}
		return nil, fmt.Errorf("файл вопросов %s не прочитан: %v; файл не изменён", path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("файл вопросов %s не прочитан: лишние данные; файл не изменён", path)
	}
	return records, nil
}
