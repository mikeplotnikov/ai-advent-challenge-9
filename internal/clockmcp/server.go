// Package clockmcp exposes deterministic Moscow calendar tools through MCP.
package clockmcp

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	ServerName    = "day20-clock"
	ServerVersion = "1.0.0"
	Timezone      = "Europe/Moscow (UTC+3)"
)

var (
	moscow = time.FixedZone("MSK", 3*60*60)
	dateRE = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
)

type Options struct {
	Now func() time.Time
}

type CurrentDateOutput struct {
	Date     string `json:"date"`
	Time     string `json:"time"`
	Weekday  string `json:"weekday"`
	Timezone string `json:"timezone"`
}

type ShiftDateInput struct {
	Date string `json:"date" jsonschema:"Календарная дата в формате ГГГГ-ММ-ДД."`
	Days int    `json:"days" jsonschema:"Целое число календарных дней для сдвига; отрицательное число сдвигает назад."`
}

type ShiftDateOutput struct {
	Date    string `json:"date"`
	Weekday string `json:"weekday"`
}

func NewServer(options Options) *mcp.Server {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	server := mcp.NewServer(&mcp.Implementation{Name: ServerName, Version: ServerVersion}, nil)
	mcp.AddTool(server, &mcp.Tool{
		Name:        "current_date",
		Description: "Возвращает текущие дату, время и день недели по Москве (UTC+3).",
	}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, CurrentDateOutput, error) {
		value := now().In(moscow)
		return nil, CurrentDateOutput{
			Date: value.Format("2006-01-02"), Time: value.Format("15:04"),
			Weekday: russianWeekday(value.Weekday()), Timezone: Timezone,
		}, nil
	})
	mcp.AddTool(server, &mcp.Tool{
		Name: "shift_date",
		Description: "Сдвигает календарную дату на целое число дней. Для относительных периодов " +
			"(«неделю назад», «последние 14 дней») вызывайте этот инструмент вместо вычисления в уме. " +
			"«Последние N дней» включают сегодняшний день: начало периода — сдвиг на −(N−1).",
	}, func(_ context.Context, _ *mcp.CallToolRequest, input ShiftDateInput) (*mcp.CallToolResult, ShiftDateOutput, error) {
		value, err := parseDate(input.Date)
		if err != nil {
			return nil, ShiftDateOutput{}, err
		}
		if input.Days < -3660 || input.Days > 3660 {
			return nil, ShiftDateOutput{}, fmt.Errorf("число дней должно быть в диапазоне от -3660 до 3660")
		}
		shifted := value.AddDate(0, 0, input.Days)
		if shifted.Year() < 1 || shifted.Year() > 9999 {
			return nil, ShiftDateOutput{}, fmt.Errorf("результат выходит за допустимый диапазон дат 0001-01-01…9999-12-31")
		}
		return nil, ShiftDateOutput{Date: shifted.Format("2006-01-02"), Weekday: russianWeekday(shifted.Weekday())}, nil
	})
	return server
}

func parseDate(raw string) (time.Time, error) {
	if !dateRE.MatchString(raw) {
		return time.Time{}, fmt.Errorf("неверный формат даты %q: ожидается ГГГГ-ММ-ДД", raw)
	}
	value, err := time.ParseInLocation("2006-01-02", raw, moscow)
	if err != nil {
		return time.Time{}, fmt.Errorf("несуществующая календарная дата %q", raw)
	}
	return value, nil
}

func russianWeekday(day time.Weekday) string {
	return [...]string{"воскресенье", "понедельник", "вторник", "среда", "четверг", "пятница", "суббота"}[day]
}
