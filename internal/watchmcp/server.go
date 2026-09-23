// Package watchmcp exposes scheduled CBR observations as MCP tools.
package watchmcp

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/cbr"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/watch"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Options struct {
	Store *watch.Store
	CBR   cbr.Options
	Now   func() time.Time
}

type CreateWatchInput struct {
	Codes        []string `json:"codes" jsonschema:"Коды валют ISO 4217, от 1 до 10 различных кодов"`
	EveryMinutes int      `json:"every_minutes" jsonschema:"Период опроса в минутах, целое число от 1 до 1440"`
}

type EmptyInput struct{}

type StopWatchInput struct {
	WatchID string `json:"watch_id" jsonschema:"Идентификатор наблюдения, например w1"`
}

type SummaryInput struct {
	WatchID string `json:"watch_id,omitempty" jsonschema:"Идентификатор наблюдения; без него возвращаются все активные"`
	// A pointer, so an explicit 0 is told apart from an omitted field: 0 is out of range,
	// omitted (or null) means the default window.
	Hours *int `json:"hours,omitempty" jsonschema:"Окно агрегации в целых часах от 1 до 720; по умолчанию 24"`
}

type ListWatchesOutput struct {
	Now     string            `json:"now"`
	Watches []watch.WatchView `json:"watches"`
}

func NewServer(options Options) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "cbr-watch", Version: "1.0.0"}, nil)
	now := options.Now
	if now == nil {
		now = time.Now
	}
	mcp.AddTool(server, &mcp.Tool{Name: "create_watch", Description: "Создать периодическое наблюдение за официальными курсами ЦБ РФ"}, func(ctx context.Context, _ *mcp.CallToolRequest, in CreateWatchInput) (*mcp.CallToolResult, watch.WatchView, error) {
		codes := normalizeCodes(in.Codes)
		if len(codes) < 1 || len(codes) > 10 {
			return nil, watch.WatchView{}, fmt.Errorf("нужно от 1 до 10 различных кодов валют")
		}
		if in.EveryMinutes < 1 || in.EveryMinutes > 1440 {
			return nil, watch.WatchView{}, fmt.Errorf("every_minutes должен быть целым числом от 1 до 1440")
		}
		at := now()
		poll, rates, err := watch.FetchPoll(ctx, options.CBR, codes, at)
		if err != nil {
			return nil, watch.WatchView{}, fmt.Errorf("ЦБ недоступен, наблюдение не создано: %v", err)
		}
		if len(poll.Missing) > 0 {
			return nil, watch.WatchView{}, fmt.Errorf("коды не найдены в ответе ЦБ: %s; доступны: %s", strings.Join(poll.Missing, ", "), strings.Join(watch.AvailableCodes(rates), ", "))
		}
		created, err := options.Store.Create(codes, in.EveryMinutes, at, poll)
		if err != nil {
			return nil, watch.WatchView{}, err
		}
		return nil, watch.View(created), nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "list_watches", Description: "Список наблюдений со статусом и последним опросом"}, func(_ context.Context, _ *mcp.CallToolRequest, _ EmptyInput) (*mcp.CallToolResult, ListWatchesOutput, error) {
		state, err := options.Store.Read()
		if err != nil {
			return nil, ListWatchesOutput{}, err
		}
		result := ListWatchesOutput{Now: now().UTC().Format(time.RFC3339), Watches: make([]watch.WatchView, 0, len(state.Watches))}
		for _, current := range state.Watches {
			result.Watches = append(result.Watches, watch.View(current))
		}
		return nil, result, nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "stop_watch", Description: "Остановить наблюдение; собранные данные сохраняются"}, func(_ context.Context, _ *mcp.CallToolRequest, in StopWatchInput) (*mcp.CallToolResult, watch.WatchView, error) {
		id := strings.TrimSpace(in.WatchID)
		if id == "" {
			return nil, watch.WatchView{}, fmt.Errorf("watch_id обязателен")
		}
		stopped, err := options.Store.Stop(id, now())
		if err != nil {
			return nil, watch.WatchView{}, err
		}
		return nil, watch.View(stopped), nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "get_watch_summary", Description: "Агрегированная сводка по наблюдению за окно"}, func(_ context.Context, _ *mcp.CallToolRequest, in SummaryInput) (*mcp.CallToolResult, watch.SummaryResponse, error) {
		hours := 24
		if in.Hours != nil {
			hours = *in.Hours
		}
		if hours < 1 || hours > 720 {
			return nil, watch.SummaryResponse{}, fmt.Errorf("hours должен быть целым числом от 1 до 720")
		}
		state, err := options.Store.Read()
		if err != nil {
			return nil, watch.SummaryResponse{}, err
		}
		result, err := watch.Summaries(state, strings.TrimSpace(in.WatchID), hours, now())
		if err != nil {
			return nil, watch.SummaryResponse{}, err
		}
		return nil, result, nil
	})
	return server
}

func normalizeCodes(input []string) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(input))
	for _, raw := range input {
		code := strings.ToUpper(strings.TrimSpace(raw))
		if code == "" || seen[code] {
			continue
		}
		seen[code] = true
		result = append(result, code)
	}
	return result
}

func ActiveIDs(state watch.File) []string {
	ids := make([]string, 0)
	for _, current := range state.Watches {
		if current.Status == "active" {
			ids = append(ids, current.ID)
		}
	}
	sort.Strings(ids)
	return ids
}
