package clockmcp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/mcpclient"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestClockGoldensAndErrors(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.FixedZone("test", 3*60*60))
	session, closeFn := openClock(t, Options{Now: func() time.Time { return now }})
	defer closeFn()
	listed, err := session.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tools) != 2 || listed.Tools[0].Name != "current_date" || listed.Tools[1].Name != "shift_date" {
		t.Fatalf("unexpected tools: %#v", listed.Tools)
	}
	current := callClock(t, session, "current_date", map[string]any{})
	if got := mcpclient.ToolText(current); !strings.Contains(got, `"date":"2026-09-25"`) ||
		!strings.Contains(got, `"time":"12:00"`) || !strings.Contains(got, `"weekday":"пятница"`) ||
		!strings.Contains(got, Timezone) {
		t.Fatalf("current_date: %s", got)
	}
	cases := []struct {
		date string
		days int
		want string
	}{
		{"2026-09-25", -13, "2026-09-12"}, {"2026-09-25", -7, "2026-09-18"},
		{"2026-09-25", -1, "2026-09-24"}, {"2026-09-25", 0, "2026-09-25"},
		{"2026-03-01", -1, "2026-02-28"}, {"2026-01-01", -1, "2025-12-31"},
		{"2024-02-28", 1, "2024-02-29"},
	}
	for _, test := range cases {
		result := callClock(t, session, "shift_date", map[string]any{"date": test.date, "days": test.days})
		if result.IsError || !strings.Contains(mcpclient.ToolText(result), test.want) {
			t.Errorf("shift %s %d: %s", test.date, test.days, mcpclient.ToolText(result))
		}
	}
	errors := []struct {
		args     map[string]any
		contains string
	}{
		{map[string]any{"date": "25.09.2026", "days": 1}, "неверный формат даты"},
		{map[string]any{"date": "2026-02-30", "days": 1}, "несуществующая календарная дата"},
		{map[string]any{"date": "2026-09-25", "days": 3661}, "диапазоне от -3660 до 3660"},
		{map[string]any{"date": "9999-12-31", "days": 1}, "допустимый диапазон дат"},
		{map[string]any{"date": "2026-09-25"}, "validating"},
		{map[string]any{"date": "2026-09-25", "days": 1.5}, "validating"},
	}
	for _, test := range errors {
		result := callClock(t, session, "shift_date", test.args)
		if !result.IsError || !strings.Contains(mcpclient.ToolText(result), test.contains) {
			t.Errorf("args %#v: isError=%v text=%s", test.args, result.IsError, mcpclient.ToolText(result))
		}
	}
}

func openClock(t *testing.T, options Options) (*mcpclient.Session, func()) {
	t.Helper()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := NewServer(options).Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	session, err := mcpclient.NewNamed("clock-test", "1").Open(context.Background(),
		mcpclient.Transport{MCP: clientTransport})
	if err != nil {
		t.Fatal(err)
	}
	return session, func() { _ = session.Close(); _ = serverSession.Close() }
}

func callClock(t *testing.T, session *mcpclient.Session, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(context.Background(), name, args)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
