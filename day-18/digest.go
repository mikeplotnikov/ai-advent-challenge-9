package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/mcpclient"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/toolagent"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/watch"
)

const maxDigests = 200

type DigestToolCall struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
	IsError   bool           `json:"is_error"`
}

type TokenCall struct{ Prompt, Cached, Output int }

func (t TokenCall) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Prompt int `json:"prompt"`
		Cached int `json:"cached"`
		Output int `json:"output"`
	}{t.Prompt, t.Cached, t.Output})
}

type DigestTokens struct {
	Prompt  int         `json:"prompt"`
	Cached  int         `json:"cached"`
	Output  int         `json:"output"`
	PerCall []TokenCall `json:"per_call"`
}

type DigestChecks struct {
	CalledSummary   bool `json:"called_summary"`
	QuotesLastRates bool `json:"quotes_last_rates"`
}

type Digest struct {
	At          string                 `json:"at"`
	SlotStart   string                 `json:"slot_start"`
	DigestEvery string                 `json:"digest_every"`
	WindowHours int                    `json:"window_hours"`
	Text        string                 `json:"text"`
	Error       string                 `json:"error"`
	Model       string                 `json:"model"`
	ModelCalls  int                    `json:"model_calls"`
	ToolCalls   []DigestToolCall       `json:"tool_calls"`
	Summary     *watch.SummaryResponse `json:"summary"`
	Tokens      DigestTokens           `json:"tokens"`
	Cost        float64                `json:"cost"`
	CostKnown   bool                   `json:"cost_known"`
	Checks      DigestChecks           `json:"checks"`
}

type digestSession interface{ toolagent.Session }

func runTick(ctx context.Context, session digestSession, model toolagent.LLM, path string, every time.Duration, windowHours int, stdout, stderr io.Writer) int {
	result := issueDigest(ctx, session, model, path, every, windowHours, time.Now, stdout, stderr)
	if result.busy || result.already || result.noActive {
		return 0
	}
	if result.err != nil {
		return 1
	}
	return 0
}

type digestResult struct {
	busy, already, noActive bool
	modelFailure            bool
	err                     error
}

func issueDigest(ctx context.Context, session digestSession, model toolagent.LLM, path string, every time.Duration, windowHours int, now func() time.Time, stdout, stderr io.Writer) digestResult {
	result := digestResult{}
	busy, err := withDigestLock(path, func() error {
		digests, err := readDigests(path)
		if err != nil {
			return err
		}
		current := now()
		for _, digest := range digests {
			at, parseErr := time.Parse(time.RFC3339, digest.At)
			if parseErr == nil && watch.Slot(at, every) == watch.Slot(current, every) {
				fmt.Fprintf(stdout, "сводка в этом слоте уже есть (%s)\n", at.In(time.FixedZone("MSK", 3*3600)).Format("02.01 15:04 МСК"))
				result.already = true
				return nil
			}
		}
		active, err := activeWatches(ctx, session)
		if err != nil {
			return fmt.Errorf("MCP-сервер недоступен: %w", err)
		}
		if active == 0 {
			fmt.Fprintln(stdout, "активных наблюдений нет — сводка не нужна")
			result.noActive = true
			return nil
		}
		question := fmt.Sprintf("Составь сводку по всем активным наблюдениям за последние %d ч", windowHours)
		trace, runErr := toolagent.Run(ctx, model, session, toolagent.Input{SystemPrompt: promptNow(current), Question: question})
		digest := digestFromTrace(trace, current, every, windowHours, runErr)
		if len(digests) >= maxDigests {
			digests = append([]Digest(nil), digests[len(digests)-maxDigests+1:]...)
		}
		digests = append(digests, digest)
		if err := writeDigests(path, digests); err != nil {
			return err
		}
		if digest.Text != "" {
			fmt.Fprintf(stdout, "[сводка %s · окно %d ч]\n%s\n", current.In(time.FixedZone("MSK", 3*3600)).Format("02.01 15:04 МСК"), windowHours, safeMultiline(digest.Text))
		}
		printSummary(stderr, trace)
		if runErr != nil {
			fmt.Fprintln(stderr, safeMultiline(runErr.Error()))
			result.modelFailure = true
			return runErr
		}
		return nil
	})
	if busy {
		fmt.Fprintln(stdout, "другой процесс уже выпускает сводку")
		result.busy = true
		return result
	}
	if err != nil {
		fmt.Fprintln(stderr, safeMultiline(err.Error()))
		result.err = err
	}
	return result
}

func activeWatches(ctx context.Context, session digestSession) (int, error) {
	response, err := session.CallTool(ctx, "list_watches", map[string]any{})
	if err != nil {
		return 0, err
	}
	if response.IsError {
		return 0, errors.New(mcpclient.ToolText(response))
	}
	var listed struct {
		Watches []struct {
			Status string `json:"status"`
		} `json:"watches"`
	}
	if err := decodeToolResult(response.StructuredContent, mcpclient.ToolText(response), &listed); err != nil {
		return 0, err
	}
	active := 0
	for _, current := range listed.Watches {
		if current.Status == "active" {
			active++
		}
	}
	return active, nil
}

func decodeToolResult(structured any, text string, target any) error {
	var raw []byte
	var err error
	if structured != nil {
		raw, err = json.Marshal(structured)
	} else {
		raw = []byte(text)
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, target)
}

func digestFromTrace(trace toolagent.Trace, at time.Time, every time.Duration, windowHours int, runErr error) Digest {
	digest := Digest{At: at.UTC().Format(time.RFC3339), SlotStart: watch.SlotStart(at, every).Format(time.RFC3339), DigestEvery: every.String(), WindowHours: windowHours, Text: trace.FinalAnswer, ModelCalls: trace.Totals.ModelCalls, ToolCalls: []DigestToolCall{}, Tokens: DigestTokens{Prompt: trace.Totals.PromptTokens, Cached: trace.Totals.CachedTokens, Output: trace.Totals.OutputTokens, PerCall: []TokenCall{}}, Cost: trace.Totals.Cost, CostKnown: trace.Totals.CostKnown}
	if runErr != nil {
		digest.Error = runErr.Error()
		digest.Text = ""
	}
	for _, call := range trace.ModelCalls {
		digest.Model = call.Model
		digest.Tokens.PerCall = append(digest.Tokens.PerCall, TokenCall{call.Usage.PromptTokens, call.Usage.PromptCacheHitTokens, call.Usage.CompletionTokens})
	}
	for _, call := range trace.ToolCalls {
		digest.ToolCalls = append(digest.ToolCalls, DigestToolCall{Name: call.Name, Arguments: call.Arguments, IsError: call.IsError})
		if call.Name != "get_watch_summary" || call.IsError || call.Rejected != "" || call.ParseError != "" || call.Structured == nil {
			continue
		}
		var summary watch.SummaryResponse
		raw, err := json.Marshal(call.Structured)
		if err == nil && json.Unmarshal(raw, &summary) == nil {
			digest.Summary = &summary
			digest.Checks.CalledSummary = true
		}
	}
	if digest.Checks.CalledSummary && digest.Text != "" {
		digest.Checks.QuotesLastRates = true
		for _, summary := range digest.Summary.Watches {
			for _, currency := range summary.Currencies {
				if !usesValue(digest.Text, currency.Last.UnitRate) {
					digest.Checks.QuotesLastRates = false
				}
			}
		}
	}
	if runErr != nil {
		digest.Checks = DigestChecks{}
	}
	return digest
}

func runDaemon(ctx context.Context, session digestSession, model toolagent.LLM, path string, every time.Duration, windowHours int, timeout, checkEvery time.Duration, stdout, stderr io.Writer) int {
	check := func() int {
		if ctx.Err() != nil {
			return 0
		}
		if _, err := activeWatches(ctx, session); err != nil {
			fmt.Fprintln(stderr, "MCP-сервер недоступен:", safeMultiline(err.Error()))
			return 1
		}
		turnCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		result := issueDigest(turnCtx, session, model, path, every, windowHours, time.Now, stdout, stderr)
		if ctx.Err() != nil {
			return 0
		}
		if result.err != nil && result.modelFailure {
			if _, err := activeWatches(ctx, session); err != nil {
				fmt.Fprintln(stderr, "MCP-сервер недоступен:", safeMultiline(err.Error()))
				return 1
			}
			return 0
		}
		if result.err != nil {
			return 1
		}
		return 0
	}
	if code := check(); code != 0 {
		return code
	}
	ticker := time.NewTicker(checkEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return 0
		case <-ticker.C:
			if code := check(); code != 0 {
				return code
			}
		}
	}
}

func withDigestLock(path string, fn func() error) (bool, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return false, err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return true, nil
		}
		return false, err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	return false, fn()
}

func readDigests(path string) ([]Digest, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return []Digest{}, nil
	}
	if err != nil {
		return nil, err
	}
	var digests []Digest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&digests); err != nil || digests == nil {
		if err == nil {
			err = errors.New("ожидался JSON-массив")
		}
		return nil, fmt.Errorf("файл сводок %s не прочитан: %v; файл не изменён", path, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("файл сводок %s не прочитан: лишние данные; файл не изменён", path)
	}
	return digests, nil
}

func writeDigests(path string, digests []Digest) error {
	raw, err := json.MarshalIndent(digests, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	fail := func(cause error) error { _ = tmp.Close(); return cause }
	if err := tmp.Chmod(0o600); err != nil {
		return fail(err)
	}
	if _, err := tmp.Write(raw); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
