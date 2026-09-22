// Package ratesmcp exposes official CBR rates through an MCP server.
package ratesmcp

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/cbr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

//go:embed fixtures/*.xml
var Fixtures embed.FS

type Options struct {
	CBR cbr.Options
}

type getRatesInput struct {
	Date  string   `json:"date,omitempty" jsonschema:"Дата курса в формате ГГГГ-ММ-ДД; если не указана, берётся последний курс."`
	Codes []string `json:"codes,omitempty" jsonschema:"Коды валют ISO 4217; если не указаны или пусты, возвращаются все валюты."`
}

type convertInput struct {
	Amount float64 `json:"amount" jsonschema:"Сумма для конвертации, должна быть больше нуля."`
	From   string  `json:"from" jsonschema:"Исходная валюта ISO 4217 или RUB."`
	To     string  `json:"to" jsonschema:"Целевая валюта ISO 4217 или RUB."`
	Date   string  `json:"date,omitempty" jsonschema:"Дата курса в формате ГГГГ-ММ-ДД; если не указана, берётся последний курс."`
}

type rateOutput struct {
	Code     string  `json:"code"`
	Name     string  `json:"name"`
	Nominal  int     `json:"nominal"`
	Value    float64 `json:"value"`
	UnitRate float64 `json:"unit_rate"`
}

type getRatesOutput struct {
	RequestedDate string       `json:"requested_date"`
	RatesDate     string       `json:"rates_date"`
	Source        string       `json:"source"`
	Note          string       `json:"note"`
	Rates         []rateOutput `json:"rates"`
}

type convertOutput struct {
	Amount        float64 `json:"amount"`
	From          string  `json:"from"`
	To            string  `json:"to"`
	Result        float64 `json:"result"`
	Rate          float64 `json:"rate"`
	RatesDate     string  `json:"rates_date"`
	RequestedDate string  `json:"requested_date"`
	Note          string  `json:"note"`
	Source        string  `json:"source"`
}

func NewServer(options Options) *mcp.Server {
	rates := cbr.New(options.CBR)
	server := mcp.NewServer(&mcp.Implementation{Name: "cbr-rates", Version: "1.0.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "get_currency_rates", Description: "Возвращает официальные курсы ЦБ РФ к рублю на дату ГГГГ-ММ-ДД; всегда берите курсы из этого инструмента, а не из памяти."}, func(ctx context.Context, _ *mcp.CallToolRequest, input getRatesInput) (*mcp.CallToolResult, getRatesOutput, error) {
		result, err := rates.Get(ctx, input.Date)
		if err != nil {
			return nil, getRatesOutput{}, toolError(err)
		}
		available := byCode(result.Currencies)
		selected := result.Currencies
		if len(input.Codes) > 0 {
			selected = make([]cbr.Currency, 0, len(input.Codes))
			for _, code := range input.Codes {
				currency, ok := available[strings.ToUpper(code)]
				if !ok {
					return nil, getRatesOutput{}, unknownCode(code, available)
				}
				selected = append(selected, currency)
			}
		}
		return nil, ratesOutput(result, selected), nil
	})
	mcp.AddTool(server, &mcp.Tool{Name: "convert_currency", Description: "Конвертирует сумму по официальным курсам ЦБ РФ к рублю на дату ГГГГ-ММ-ДД; RUB разрешён, всегда берите курсы из этого инструмента, а не из памяти."}, func(ctx context.Context, _ *mcp.CallToolRequest, input convertInput) (*mcp.CallToolResult, convertOutput, error) {
		if input.Amount <= 0 {
			return nil, convertOutput{}, errors.New("сумма должна быть больше нуля")
		}
		result, err := rates.Get(ctx, input.Date)
		if err != nil {
			return nil, convertOutput{}, toolError(err)
		}
		available := byCode(result.Currencies)
		from, to := strings.ToUpper(input.From), strings.ToUpper(input.To)
		fromRate, ok := unitRate(from, available)
		if !ok {
			return nil, convertOutput{}, unknownCode(input.From, available)
		}
		toRate, ok := unitRate(to, available)
		if !ok {
			return nil, convertOutput{}, unknownCode(input.To, available)
		}
		return nil, convertOutput{Amount: input.Amount, From: from, To: to, Result: cbr.Round4((input.Amount * fromRate) / toRate), Rate: cbr.Round6(fromRate / toRate), RatesDate: result.RatesDate, RequestedDate: result.RequestedDate, Note: result.Note, Source: "cbr.ru XML_daily"}, nil
	})
	return server
}

func ratesOutput(result cbr.Rates, currencies []cbr.Currency) getRatesOutput {
	output := getRatesOutput{RequestedDate: result.RequestedDate, RatesDate: result.RatesDate, Source: "cbr.ru XML_daily", Note: result.Note, Rates: make([]rateOutput, 0, len(currencies))}
	for _, currency := range currencies {
		output.Rates = append(output.Rates, rateOutput{Code: currency.Code, Name: currency.Name, Nominal: currency.Nominal, Value: currency.Value, UnitRate: currency.UnitRate})
	}
	return output
}

func byCode(currencies []cbr.Currency) map[string]cbr.Currency {
	result := make(map[string]cbr.Currency, len(currencies))
	for _, currency := range currencies {
		result[currency.Code] = currency
	}
	return result
}

func unitRate(code string, available map[string]cbr.Currency) (float64, bool) {
	if code == "RUB" {
		return 1, true
	}
	currency, ok := available[code]
	return currency.UnitRate, ok
}

func unknownCode(code string, available map[string]cbr.Currency) error {
	codes := make([]string, 0, len(available))
	for code := range available {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	return fmt.Errorf("неизвестный код валюты %q; доступны: %s", strings.ToUpper(code), strings.Join(codes, ", "))
}

func toolError(err error) error {
	var source *cbr.SourceError
	if errors.As(err, &source) {
		return fmt.Errorf("ЦБ недоступен: %w", source.Err)
	}
	return err
}
