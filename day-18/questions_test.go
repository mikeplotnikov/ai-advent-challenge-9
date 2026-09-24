package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/toolagent"
)

func TestQuestionRecordFieldsTruncationAndErrorSanitizing(t *testing.T) {
	long := strings.Repeat("я", maxToolResultRunes+100)
	trace := toolagent.Trace{
		FinalAnswer: "ответ",
		ModelCalls: []toolagent.ModelCall{
			{Model: "deepseek-flash", Usage: llm.Usage{PromptTokens: 10, PromptCacheHitTokens: 2, CompletionTokens: 3}},
			{Model: "deepseek-flash", Usage: llm.Usage{PromptTokens: 20, PromptCacheHitTokens: 4, CompletionTokens: 5}},
		},
		ToolCalls: []toolagent.ToolCall{
			{Step: 1, Name: "get_watch_summary", Arguments: map[string]any{"hours": 24}, ResultText: long, IsError: true},
			{Step: 2, Name: "missing", Rejected: "инструмент не существует", ResultText: "must not be stored"},
		},
		Totals: toolagent.Totals{ModelCalls: 2, PromptTokens: 30, CachedTokens: 6, OutputTokens: 8, Cost: 0.001, CostKnown: true},
	}
	at := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	record := questionFromTrace("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", at, "вопрос", trace, nil)
	if record.At != at.Format(time.RFC3339) || record.Answer != "ответ" || record.Error != "" || record.Model != "deepseek-flash" || record.ModelCalls != 2 || len(record.Tokens.PerCall) != 2 || record.Tokens.Prompt != 30 || record.Tokens.Cached != 6 || record.Tokens.Output != 8 || !record.CostKnown || record.Cost != 0.001 {
		t.Fatalf("record=%+v", record)
	}
	if record.Tokens.PerCall[0] != (TokenCall{10, 2, 3}) || record.Tokens.PerCall[1] != (TokenCall{20, 4, 5}) {
		t.Fatalf("per_call=%+v", record.Tokens.PerCall)
	}
	if record.ToolCalls[0].Step != 1 || record.ToolCalls[0].Name != "get_watch_summary" || record.ToolCalls[0].Arguments["hours"] != 24 || !record.ToolCalls[0].IsError {
		t.Fatalf("tool call=%+v", record.ToolCalls[0])
	}
	if utf8.RuneCountInString(record.ToolCalls[0].Result) != maxToolResultRunes || !strings.HasSuffix(record.ToolCalls[0].Result, truncatedMarker) {
		t.Fatalf("truncated len=%d suffix=%q", utf8.RuneCountInString(record.ToolCalls[0].Result), record.ToolCalls[0].Result[len(record.ToolCalls[0].Result)-32:])
	}
	if record.ToolCalls[1].Rejected == "" || record.ToolCalls[1].Result != "" {
		t.Fatalf("rejected=%+v", record.ToolCalls[1])
	}
	failed := questionFromTrace("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", at, "вопрос", trace, errors.New("API вернул 500 Internal Server Error: Bearer secret response body"))
	if failed.Answer != "" || failed.Error != "API вернул 500 Internal Server Error" || strings.Contains(failed.Error, "Bearer") || strings.Contains(failed.Error, "secret") {
		t.Fatalf("failed=%+v", failed)
	}
}

func TestWriteJSONAtomicReplacesFileWithoutChangingOpenReader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	old := []byte("old complete contents\n")
	if err := os.WriteFile(path, old, 0o600); err != nil {
		t.Fatal(err)
	}
	reader, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	before, err := reader.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSONAtomic(path, map[string]any{"new": strings.Repeat("value", 1000)}); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(before, after) {
		t.Fatal("state file was overwritten in place instead of atomically replaced")
	}
	stillOld, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(stillOld, old) {
		t.Fatalf("open reader saw partial/new contents: %q err=%v", stillOld, err)
	}
	current, err := os.ReadFile(path)
	if err != nil || !bytes.Contains(current, []byte(`"new"`)) || !json.Valid(current) {
		t.Fatalf("replacement=%q err=%v", current, err)
	}
}

func TestQuestionsCapAndCorruptFileRemainUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "questions.json")
	for i := 0; i < maxQuestions+1; i++ {
		record := QuestionRecord{RequestID: fmt.Sprintf("%032x", i), ToolCalls: []QuestionToolCall{}, Tokens: DigestTokens{PerCall: []TokenCall{}}}
		if err := appendQuestion(path, record); err != nil {
			t.Fatal(err)
		}
	}
	records, err := readQuestions(path)
	if err != nil || len(records) != maxQuestions || records[0].RequestID != fmt.Sprintf("%032x", 1) {
		t.Fatalf("len=%d first=%q err=%v", len(records), records[0].RequestID, err)
	}
	beforeLock, _ := os.ReadFile(path)
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if err := appendQuestion(path, QuestionRecord{}); err == nil || !strings.Contains(err.Error(), "другой процесс") {
		t.Fatalf("occupied lock error=%v", err)
	}
	_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	_ = lock.Close()
	afterLock, _ := os.ReadFile(path)
	if !bytes.Equal(beforeLock, afterLock) {
		t.Fatal("questions changed while lock was occupied")
	}
	corrupt := []byte("not json")
	if err := os.WriteFile(path, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := appendQuestion(path, QuestionRecord{}); err == nil || !strings.Contains(err.Error(), "файл не изменён") {
		t.Fatalf("error=%v", err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(after, corrupt) {
		t.Fatalf("corrupt file changed: %q", after)
	}
}

// Exactly 4000 characters are «not longer than 4000» and stay verbatim; one more is truncated
// to 4000 with the marker (test review, wave 1: the boundary itself was untested).
func TestToolResultTruncationBoundary(t *testing.T) {
	exact := strings.Repeat("ж", maxToolResultRunes)
	if got := truncateToolResult(exact); got != exact {
		t.Fatalf("a result of exactly %d runes was changed", maxToolResultRunes)
	}
	over := exact + "ж"
	got := truncateToolResult(over)
	if !strings.HasSuffix(got, truncatedMarker) || utf8.RuneCountInString(got) != maxToolResultRunes {
		t.Fatalf("over the limit: %d runes, suffix marker %v", utf8.RuneCountInString(got), strings.HasSuffix(got, truncatedMarker))
	}
}
