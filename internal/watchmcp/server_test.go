package watchmcp

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/cbr"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/mcpclient"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/ratesmcp"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/watch"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func fixtureFetcher(t *testing.T) cbr.Fetcher {
	t.Helper()
	raw, err := ratesmcp.Fixtures.ReadFile("fixtures/2026-09-01.xml")
	if err != nil {
		t.Fatal(err)
	}
	return cbr.FetchFunc(func(context.Context, string) ([]byte, error) { return raw, nil })
}

func openTestServer(t *testing.T, options Options) *mcp.ClientSession {
	t.Helper()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	server := NewServer(options)
	go func() { _ = server.Run(ctx, serverTransport) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func call(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestToolSchemas(t *testing.T) {
	store := watch.NewStore(filepath.Join(t.TempDir(), "store.json"))
	session := openTestServer(t, Options{Store: store, CBR: cbr.Options{Fetcher: fixtureFetcher(t)}})
	listed, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tools) != 4 {
		t.Fatalf("tools=%d", len(listed.Tools))
	}
	wantRequired := map[string][]string{"create_watch": {"codes", "every_minutes"}, "list_watches": {}, "stop_watch": {"watch_id"}, "get_watch_summary": {}}
	wantProperties := map[string][]string{"create_watch": {"codes", "every_minutes"}, "list_watches": {}, "stop_watch": {"watch_id"}, "get_watch_summary": {"hours", "watch_id"}}
	for _, tool := range listed.Tools {
		if tool.Name == "" || tool.Description == "" {
			t.Fatalf("empty metadata: %+v", tool)
		}
		raw, _ := json.Marshal(tool.InputSchema)
		var schema struct {
			Type       string   `json:"type"`
			Required   []string `json:"required"`
			Properties map[string]struct {
				Description string `json:"description"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		if schema.Type != "object" {
			t.Fatalf("%s type=%s", tool.Name, schema.Type)
		}
		sort.Strings(schema.Required)
		expected, ok := wantRequired[tool.Name]
		if !ok {
			t.Fatalf("unexpected %s", tool.Name)
		}
		sort.Strings(expected)
		if strings.Join(schema.Required, ",") != strings.Join(expected, ",") {
			t.Fatalf("%s required=%v", tool.Name, schema.Required)
		}
		for property, value := range schema.Properties {
			if value.Description == "" {
				t.Errorf("%s.%s lacks description", tool.Name, property)
			}
		}
		properties := make([]string, 0, len(schema.Properties))
		for property := range schema.Properties {
			properties = append(properties, property)
		}
		sort.Strings(properties)
		if strings.Join(properties, ",") != strings.Join(wantProperties[tool.Name], ",") {
			t.Fatalf("%s properties=%v", tool.Name, properties)
		}
	}
}

func TestToolsOutcomes(t *testing.T) {
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	store := watch.NewStore(filepath.Join(t.TempDir(), "store.json"))
	session := openTestServer(t, Options{Store: store, CBR: cbr.Options{Fetcher: fixtureFetcher(t)}, Now: func() time.Time { return now }})
	created := call(t, session, "create_watch", map[string]any{"codes": []any{"usd", "EUR", "usd"}, "every_minutes": 60})
	if created.IsError {
		t.Fatal(mcpclient.ToolText(created))
	}
	var output watch.WatchView
	if err := decode(created, &output); err != nil {
		t.Fatal(err)
	}
	if output.ID != "w1" || strings.Join(output.Codes, ",") != "USD,EUR" || output.PollsTotal != 1 {
		t.Fatalf("%+v", output)
	}
	repeated := make([]any, 11)
	for i := range repeated {
		repeated[i] = "usd"
	}
	deduplicated := call(t, session, "create_watch", map[string]any{"codes": repeated, "every_minutes": 60})
	if deduplicated.IsError {
		t.Fatalf("eleven duplicate codes must normalize to one: %s", mcpclient.ToolText(deduplicated))
	}
	var duplicateOutput watch.WatchView
	if err := decode(deduplicated, &duplicateOutput); err != nil || strings.Join(duplicateOutput.Codes, ",") != "USD" {
		t.Fatalf("output=%+v err=%v", duplicateOutput, err)
	}
	if stoppedDuplicate := call(t, session, "stop_watch", map[string]any{"watch_id": duplicateOutput.ID}); stoppedDuplicate.IsError {
		t.Fatalf("stop duplicate-code watch: %s", mcpclient.ToolText(stoppedDuplicate))
	}
	persisted, err := store.Read()
	if err != nil {
		t.Fatal(err)
	}
	firstPoll := persisted.Watches[0].Polls[0]
	if !firstPoll.OK || firstPoll.RatesDate == "" || firstPoll.Rates["USD"] == 0 || firstPoll.Rates["EUR"] == 0 {
		t.Fatalf("first poll=%+v", firstPoll)
	}
	before, _ := store.Read()
	unknown := call(t, session, "create_watch", map[string]any{"codes": []any{"XXX"}, "every_minutes": 60})
	if !unknown.IsError || !strings.Contains(mcpclient.ToolText(unknown), "доступны:") {
		t.Fatalf("%v %s", unknown.IsError, mcpclient.ToolText(unknown))
	}
	after, _ := store.Read()
	if len(after.Watches) != len(before.Watches) {
		t.Fatal("unknown code created a watch")
	}
	for _, args := range []map[string]any{{"codes": []any{}, "every_minutes": 60}, {"codes": []any{"USD"}, "every_minutes": 0}, {"codes": []any{"USD"}, "every_minutes": 1441}, {"codes": []any{"A", "B", "C", "D", "E", "F", "G", "H", "I", "J", "K"}, "every_minutes": 60}} {
		if result := call(t, session, "create_watch", args); !result.IsError {
			t.Fatalf("accepted invalid: %v", args)
		}
	}
	stopped := call(t, session, "stop_watch", map[string]any{"watch_id": "w1"})
	if stopped.IsError {
		t.Fatal(mcpclient.ToolText(stopped))
	}
	repeat := call(t, session, "stop_watch", map[string]any{"watch_id": "w1"})
	if !repeat.IsError || !strings.Contains(mcpclient.ToolText(repeat), "уже остановлено") {
		t.Fatalf("%s", mcpclient.ToolText(repeat))
	}
	missing := call(t, session, "stop_watch", map[string]any{"watch_id": "w99"})
	if !missing.IsError || !strings.Contains(mcpclient.ToolText(missing), "w1") {
		t.Fatalf("%s", mcpclient.ToolText(missing))
	}
	// list_watches returns every watch, stopped ones included, with the server's clock
	// (test review, wave 2: the real handler was never called by any test).
	listed := call(t, session, "list_watches", map[string]any{})
	if listed.IsError {
		t.Fatal(mcpclient.ToolText(listed))
	}
	var watches ListWatchesOutput
	if err := decode(listed, &watches); err != nil {
		t.Fatal(err)
	}
	if watches.Now == "" || len(watches.Watches) != 2 || watches.Watches[0].ID != "w1" || watches.Watches[1].ID != "w2" {
		t.Fatalf("list_watches: %+v", watches)
	}
	for _, listedWatch := range watches.Watches {
		if listedWatch.Status != "stopped" || listedWatch.StoppedAt == "" || listedWatch.NextSlotAt != "" || listedWatch.PollsTotal != 1 {
			t.Fatalf("stopped watch in list_watches: %+v", listedWatch)
		}
	}
	summary := call(t, session, "get_watch_summary", map[string]any{"watch_id": "w1", "hours": 24})
	if summary.IsError {
		t.Fatal(mcpclient.ToolText(summary))
	}
	var aggregated watch.SummaryResponse
	if err := decode(summary, &aggregated); err != nil {
		t.Fatal(err)
	}
	if len(aggregated.Watches) != 1 || aggregated.Watches[0].Status != "stopped" || aggregated.Watches[0].Polls.Total != 1 {
		t.Fatalf("%+v", aggregated)
	}
	empty := call(t, session, "get_watch_summary", map[string]any{})
	if empty.IsError {
		t.Fatal(mcpclient.ToolText(empty))
	}
	if err := decode(empty, &aggregated); err != nil {
		t.Fatal(err)
	}
	if len(aggregated.Watches) != 0 || aggregated.Note != "активных наблюдений нет" {
		t.Fatalf("%+v", aggregated)
	}
	badSummary := call(t, session, "get_watch_summary", map[string]any{"watch_id": "w99"})
	if !badSummary.IsError || !strings.Contains(mcpclient.ToolText(badSummary), "w1") {
		t.Fatalf("%s", mcpclient.ToolText(badSummary))
	}
	// hours: omitted (or null) → the default 24-hour window; an explicit 0 and 721 are out of
	// the 1–720 range and must not be read as "use the default" (code review, wave 1).
	omitted := call(t, session, "get_watch_summary", map[string]any{"watch_id": "w1"})
	if omitted.IsError {
		t.Fatal(mcpclient.ToolText(omitted))
	}
	if err := decode(omitted, &aggregated); err != nil || len(aggregated.Watches) != 1 || aggregated.Watches[0].Hours != 24 {
		t.Fatalf("omitted hours: %+v %v", aggregated, err)
	}
	for _, hours := range []int{0, 721} {
		out := call(t, session, "get_watch_summary", map[string]any{"watch_id": "w1", "hours": hours})
		if !out.IsError || !strings.Contains(mcpclient.ToolText(out), "от 1 до 720") {
			t.Fatalf("hours=%d: %s", hours, mcpclient.ToolText(out))
		}
	}
}

func TestCreateUnavailableAndActiveLimit(t *testing.T) {
	now := func() time.Time { return time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC) }
	t.Run("unavailable", func(t *testing.T) {
		store := watch.NewStore(filepath.Join(t.TempDir(), "store.json"))
		session := openTestServer(t, Options{Store: store, CBR: cbr.Options{Fetcher: cbr.FetchFunc(func(context.Context, string) ([]byte, error) { return nil, errors.New("500") })}, Now: now})
		result := call(t, session, "create_watch", map[string]any{"codes": []any{"USD"}, "every_minutes": 60})
		if !result.IsError || !strings.Contains(mcpclient.ToolText(result), "ЦБ недоступен, наблюдение не создано") {
			t.Fatalf("%s", mcpclient.ToolText(result))
		}
		state, _ := store.Read()
		if len(state.Watches) != 0 {
			t.Fatal("created on failure")
		}
	})
	t.Run("eleventh", func(t *testing.T) {
		store := watch.NewStore(filepath.Join(t.TempDir(), "store.json"))
		poll := watch.Poll{At: now().Format(time.RFC3339), OK: true, RatesDate: "2026-09-24", Rates: map[string]float64{"USD": 80}}
		for i := 0; i < 10; i++ {
			if _, err := store.Create([]string{"USD"}, 60, now(), poll); err != nil {
				t.Fatal(err)
			}
		}
		session := openTestServer(t, Options{Store: store, CBR: cbr.Options{Fetcher: fixtureFetcher(t)}, Now: now})
		result := call(t, session, "create_watch", map[string]any{"codes": []any{"USD"}, "every_minutes": 60})
		if !result.IsError || !strings.Contains(mcpclient.ToolText(result), "w1") || !strings.Contains(mcpclient.ToolText(result), "w10") {
			t.Fatalf("%s", mcpclient.ToolText(result))
		}
		state, err := store.Read()
		if err != nil || len(state.Watches) != 10 {
			t.Fatalf("eleventh request mutated store: watches=%d err=%v", len(state.Watches), err)
		}
	})
}

func decode(result *mcp.CallToolResult, target any) error {
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, target)
}
