package ratesmcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/cbr"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestToolsListAndConversions(t *testing.T) {
	session := connect(t, fixtureFetcher(t, map[string]string{"2026-09-01": "2026-09-01.xml"}))
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 2 {
		t.Fatalf("tools/list returned %d tools", len(tools.Tools))
	}
	required := map[string][]string{"get_currency_rates": {}, "convert_currency": {"amount", "from", "to"}}
	propertiesWant := map[string][]string{"get_currency_rates": {"codes", "date"}, "convert_currency": {"amount", "date", "from", "to"}}
	for _, tool := range tools.Tools {
		if tool.Name == "" || tool.Description == "" {
			t.Fatalf("incomplete tool definition: %#v", tool)
		}
		schema := schemaMap(t, tool.InputSchema)
		if schema["type"] != "object" {
			t.Fatalf("%s schema type = %#v", tool.Name, schema["type"])
		}
		properties, ok := schema["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s has no properties", tool.Name)
		}
		for name, raw := range properties {
			property := raw.(map[string]any)
			if property["description"] == "" {
				t.Fatalf("%s.%s has no description", tool.Name, name)
			}
		}
		if got := sortedKeys(properties); strings.Join(got, ",") != strings.Join(propertiesWant[tool.Name], ",") {
			t.Fatalf("%s properties = %v", tool.Name, got)
		}
		gotRequired := stringsSlice(schema["required"])
		if strings.Join(gotRequired, ",") != strings.Join(required[tool.Name], ",") {
			t.Fatalf("%s required = %v", tool.Name, gotRequired)
		}
		for _, phrase := range []string{"официальн", "ЦБ", "рублю", "ГГГГ-ММ-ДД", "инструмента", "памяти"} {
			if !strings.Contains(tool.Description, phrase) {
				t.Fatalf("%s description does not contain %q: %q", tool.Name, phrase, tool.Description)
			}
		}
		if tool.Name == "convert_currency" && !strings.Contains(tool.Description, "RUB") {
			t.Fatalf("convert_currency description does not allow RUB: %q", tool.Description)
		}
	}

	rates := call(t, session, "get_currency_rates", map[string]any{"date": "2026-09-01", "codes": []string{"usd", "KZT"}})
	var selected getRatesOutput
	decodeResult(t, rates, &selected)
	assertJSONFields(t, rates, "requested_date", "rates_date", "source", "note", "rates")
	assertRateJSONFields(t, rates)
	if selected.RequestedDate != "2026-09-01" || selected.RatesDate != "2026-09-01" || len(selected.Rates) != 2 {
		t.Fatalf("unexpected selected rates: %#v", selected)
	}
	if selected.Rates[0] != (rateOutput{Code: "USD", Name: "Доллар США", Nominal: 1, Value: 86.3793, UnitRate: 86.3793}) {
		t.Fatalf("USD = %#v", selected.Rates[0])
	}
	if selected.Rates[1] != (rateOutput{Code: "KZT", Name: "Тенге", Nominal: 100, Value: 18.5854, UnitRate: 0.185854}) {
		t.Fatalf("KZT = %#v", selected.Rates[1])
	}
	all := call(t, session, "get_currency_rates", map[string]any{"date": "2026-09-01", "codes": []string{}})
	var allRates getRatesOutput
	decodeResult(t, all, &allRates)
	absent := call(t, session, "get_currency_rates", map[string]any{"date": "2026-09-01"})
	var absentRates getRatesOutput
	decodeResult(t, absent, &absentRates)
	wantCodes := fixtureCodes(t)
	if got := outputCodes(allRates.Rates); strings.Join(got, ",") != strings.Join(wantCodes, ",") {
		t.Fatalf("codes [] returned %v, want CBR order %v", got, wantCodes)
	}
	if got := outputCodes(absentRates.Rates); strings.Join(got, ",") != strings.Join(wantCodes, ",") {
		t.Fatalf("absent codes returned %v, want CBR order %v", got, wantCodes)
	}

	usd := call(t, session, "convert_currency", map[string]any{"amount": 250, "from": "USD", "to": "RUB", "date": "2026-09-01"})
	var usdOut convertOutput
	decodeResult(t, usd, &usdOut)
	assertJSONFields(t, usd, "amount", "from", "to", "result", "rate", "rates_date", "requested_date", "note", "source")
	if usdOut.Rate != 86.3793 || usdOut.Result != 21594.825 {
		t.Fatalf("USD conversion = %#v", usdOut)
	}
	one := call(t, session, "convert_currency", map[string]any{"amount": 1, "from": "usd", "to": "rub", "date": "2026-09-01"})
	var oneOut convertOutput
	decodeResult(t, one, &oneOut)
	if oneOut.From != "USD" || oneOut.To != "RUB" || oneOut.Result != 86.3793 {
		t.Fatalf("case-insensitive conversion = %#v", oneOut)
	}
	cny := call(t, session, "convert_currency", map[string]any{"amount": 1000, "from": "CNY", "to": "KZT", "date": "2026-09-01"})
	var cnyOut convertOutput
	decodeResult(t, cny, &cnyOut)
	if cnyOut.Rate != 69.183337 || cnyOut.Result != 69183.3375 {
		t.Fatalf("CNY conversion = %#v", cnyOut)
	}
	rub := call(t, session, "convert_currency", map[string]any{"amount": 100, "from": "RUB", "to": "USD", "date": "2026-09-01"})
	var rubOut convertOutput
	decodeResult(t, rub, &rubOut)
	if rubOut.Rate != 0.011577 || rubOut.Result != 1.1577 {
		t.Fatalf("RUB conversion = %#v", rubOut)
	}
	same := call(t, session, "convert_currency", map[string]any{"amount": 5, "from": "USD", "to": "USD", "date": "2026-09-01"})
	var sameOut convertOutput
	decodeResult(t, same, &sameOut)
	if sameOut.Rate != 1 || sameOut.Result != 5 {
		t.Fatalf("same-currency conversion = %#v", sameOut)
	}
}

func TestToolErrorsDatesAndCache(t *testing.T) {
	clock := func() time.Time { return time.Date(2026, 9, 22, 12, 0, 0, 0, cbr.Moscow) }
	var requests atomic.Int32
	base := cbr.FetchFunc(func(_ context.Context, requested string) ([]byte, error) {
		requests.Add(1)
		name := "2026-09-01.xml"
		if requested == "2026-09-20" {
			name = "2026-09-20-weekend.xml"
		}
		return fixture(t, name), nil
	})
	session := connect(t, cbr.Options{Fetcher: base, Now: clock})
	for _, args := range []map[string]any{
		{"date": "2026-09-23"},
		{},
		{"date": ""},
	} {
		result := call(t, session, "get_currency_rates", args)
		if result.IsError {
			t.Fatalf("valid date call failed: %s", resultText(result))
		}
	}
	assertToolError(t, call(t, session, "get_currency_rates", map[string]any{"date": "2026-09-24"}), "курс на 24.09.2026 ЦБ ещё не устанавливал: ЦБ устанавливает курс накануне, последний возможный — на завтра, 23.09.2026")
	assertToolError(t, call(t, session, "get_currency_rates", map[string]any{"date": "1997-12-31"}), "курсы до 01.01.1998 даны в рублях до деноминации; поддерживаются даты с 1998-01-01")
	assertToolError(t, call(t, session, "get_currency_rates", map[string]any{"date": "2026-13-01"}), "некорректная дата \"2026-13-01\": нужен формат ГГГГ-ММ-ДД")
	unknown := call(t, session, "get_currency_rates", map[string]any{"date": "2026-09-01", "codes": []string{"XXX"}})
	assertToolError(t, unknown, "неизвестный код валюты \"XXX\"; доступны: "+availableCodes(t))
	assertToolError(t, call(t, session, "convert_currency", map[string]any{"amount": 0, "from": "USD", "to": "RUB"}), "сумма должна быть больше нуля")
	assertToolError(t, call(t, session, "convert_currency", map[string]any{"amount": -1, "from": "USD", "to": "RUB"}), "сумма должна быть больше нуля")

	weekend := call(t, session, "get_currency_rates", map[string]any{"date": "2026-09-20"})
	var weekendOut getRatesOutput
	decodeResult(t, weekend, &weekendOut)
	if weekendOut.RatesDate != "2026-09-19" || weekendOut.Note != "ЦБ не устанавливал курс на 20.09.2026 (выходной или праздник, либо курс ещё не опубликован); действует курс от 19.09.2026" {
		t.Fatalf("weekend result = %#v", weekendOut)
	}
	call(t, session, "get_currency_rates", map[string]any{"date": "2026-09-01"})
	call(t, session, "get_currency_rates", map[string]any{"date": "2026-09-01"})
	if got := requests.Load(); got != 4 { // tomorrow, latest and weekend each fetch once; the two past calls share cache.
		t.Fatalf("upstream requests = %d, want 4", got)
	}
}

func TestUpstreamAndFixtureErrorsAreToolErrors(t *testing.T) {
	for _, test := range []struct {
		name    string
		fetcher cbr.Fetcher
		want    string
	}{
		{"empty", cbr.FetchFunc(func(context.Context, string) ([]byte, error) { return fixture(t, "empty.xml"), nil }), "ЦБ не вернул курсов на эту дату"},
		{"parameters", cbr.FetchFunc(func(context.Context, string) ([]byte, error) { return fixture(t, "error-in-parameters.xml"), nil }), "ЦБ отклонил запрос: Error in parameters"},
		{"network", cbr.FetchFunc(func(context.Context, string) ([]byte, error) { return nil, errors.New("сеть недоступна") }), "ЦБ недоступен: сеть недоступна"},
	} {
		t.Run(test.name, func(t *testing.T) {
			session := connect(t, cbr.Options{Fetcher: test.fetcher})
			assertToolError(t, call(t, session, "get_currency_rates", map[string]any{"date": "2026-09-01"}), test.want)
		})
	}
	var historicalQuery string
	http500 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		historicalQuery = request.URL.Query().Get("date_req")
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer http500.Close()
	session := connect(t, cbr.Options{Fetcher: cbr.HTTPFetcher{URL: http500.URL}})
	assertToolError(t, call(t, session, "get_currency_rates", map[string]any{"date": "2026-09-01"}), "ЦБ недоступен: ЦБ ответил HTTP 500")
	if historicalQuery != "01/09/2026" {
		t.Fatalf("date_req = %q", historicalQuery)
	}
	var latestQuery string
	latest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		latestQuery = request.URL.RawQuery
		_, _ = w.Write(fixture(t, "2026-09-01.xml"))
	}))
	defer latest.Close()
	session = connect(t, cbr.Options{Fetcher: cbr.HTTPFetcher{URL: latest.URL}})
	if result := call(t, session, "get_currency_rates", map[string]any{}); result.IsError {
		t.Fatalf("latest result: %s", resultText(result))
	}
	if latestQuery != "" {
		t.Fatalf("latest query = %q, want none", latestQuery)
	}

	timeout := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { time.Sleep(100 * time.Millisecond) }))
	defer timeout.Close()
	session = connect(t, cbr.Options{Fetcher: cbr.HTTPFetcher{URL: timeout.URL, Timeout: time.Millisecond}})
	result := call(t, session, "get_currency_rates", map[string]any{"date": "2026-09-01"})
	if !result.IsError || !strings.HasPrefix(resultText(result), "ЦБ недоступен: ") || !strings.Contains(resultText(result), "Client.Timeout") {
		t.Fatalf("timeout result = isError %v text %q", result.IsError, resultText(result))
	}
	oversized := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(make([]byte, 1<<20+1))
	}))
	defer oversized.Close()
	session = connect(t, cbr.Options{Fetcher: cbr.HTTPFetcher{URL: oversized.URL}})
	assertToolError(t, call(t, session, "get_currency_rates", map[string]any{"date": "2026-09-01"}), "ЦБ недоступен: ответ ЦБ больше 1 МиБ")
}

func TestLatestCacheExpiresAfterTenMinutes(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, cbr.Moscow)
	var requests atomic.Int32
	session := connect(t, cbr.Options{
		Now: func() time.Time { return now },
		Fetcher: cbr.FetchFunc(func(context.Context, string) ([]byte, error) {
			requests.Add(1)
			return fixture(t, "2026-09-01.xml"), nil
		}),
	})
	call(t, session, "get_currency_rates", map[string]any{})
	call(t, session, "get_currency_rates", map[string]any{})
	if requests.Load() != 1 {
		t.Fatalf("latest requests before expiry = %d", requests.Load())
	}
	now = now.Add(10 * time.Minute)
	call(t, session, "get_currency_rates", map[string]any{})
	if requests.Load() != 2 {
		t.Fatalf("latest requests after expiry = %d", requests.Load())
	}
}

func TestPastRatesArePermanentAndErrorsAreNotCached(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, cbr.Moscow)
	var requests atomic.Int32
	session := connect(t, cbr.Options{
		Now: func() time.Time { return now },
		Fetcher: cbr.FetchFunc(func(context.Context, string) ([]byte, error) {
			if requests.Add(1) == 1 {
				return nil, errors.New("временный сбой")
			}
			return fixture(t, "2026-09-01.xml"), nil
		}),
	})
	assertToolError(t, call(t, session, "get_currency_rates", map[string]any{"date": "2026-09-01"}), "ЦБ недоступен: временный сбой")
	if result := call(t, session, "get_currency_rates", map[string]any{"date": "2026-09-01"}); result.IsError {
		t.Fatalf("retry after upstream error: %s", resultText(result))
	}
	now = now.AddDate(0, 0, 90)
	if result := call(t, session, "get_currency_rates", map[string]any{"date": "2026-09-01"}); result.IsError {
		t.Fatalf("cached past rate: %s", resultText(result))
	}
	if requests.Load() != 2 {
		t.Fatalf("past requests = %d, want 2", requests.Load())
	}
}

func connect(t *testing.T, options cbr.Options) *mcp.ClientSession {
	t.Helper()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	if _, err := NewServer(Options{CBR: options}).Connect(context.Background(), serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func fixtureFetcher(t *testing.T, names map[string]string) cbr.Options {
	t.Helper()
	return cbr.Options{Fetcher: cbr.FetchFunc(func(_ context.Context, date string) ([]byte, error) { return fixture(t, names[date]), nil })}
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	contents, err := Fixtures.ReadFile("fixtures/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return contents
}

func call(t *testing.T, session *mcp.ClientSession, name string, arguments map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func decodeResult(t *testing.T, result *mcp.CallToolResult, target any) {
	t.Helper()
	if result.IsError {
		t.Fatalf("unexpected tool error: %s", resultText(result))
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, target); err != nil {
		t.Fatal(err)
	}
}

func resultText(result *mcp.CallToolResult) string {
	var parts []string
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func assertToolError(t *testing.T, result *mcp.CallToolResult, want string) {
	t.Helper()
	if !result.IsError || resultText(result) != want {
		t.Fatalf("error = isError %v text %q, want %q", result.IsError, resultText(result), want)
	}
}

func schemaMap(t *testing.T, value any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func stringsSlice(value any) []string {
	values, _ := value.([]any)
	result := make([]string, len(values))
	for i, value := range values {
		result[i], _ = value.(string)
	}
	return result
}

func availableCodes(t *testing.T) string {
	t.Helper()
	rates, err := cbr.New(cbr.Options{Fetcher: cbr.FetchFunc(func(context.Context, string) ([]byte, error) { return fixture(t, "2026-09-01.xml"), nil })}).Get(context.Background(), "2026-09-01")
	if err != nil {
		t.Fatal(err)
	}
	codes := make([]string, 0, len(rates.Currencies))
	for _, rate := range rates.Currencies {
		codes = append(codes, rate.Code)
	}
	sort.Strings(codes)
	return strings.Join(codes, ", ")
}

func sortedKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func outputCodes(rates []rateOutput) []string {
	codes := make([]string, len(rates))
	for i, rate := range rates {
		codes[i] = rate.Code
	}
	return codes
}

func fixtureCodes(t *testing.T) []string {
	t.Helper()
	matches := regexp.MustCompile(`<CharCode>([A-Z]+)</CharCode>`).FindAllSubmatch(fixture(t, "2026-09-01.xml"), -1)
	codes := make([]string, len(matches))
	for i, match := range matches {
		codes[i] = string(match[1])
	}
	return codes
}

func assertJSONFields(t *testing.T, result *mcp.CallToolResult, fields ...string) {
	t.Helper()
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &value); err != nil {
		t.Fatal(err)
	}
	want := append([]string(nil), fields...)
	sort.Strings(want)
	if got := sortedRawKeys(value); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("structured fields = %v, want %v", got, want)
	}
}

func assertRateJSONFields(t *testing.T, result *mcp.CallToolResult) {
	t.Helper()
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &root); err != nil {
		t.Fatal(err)
	}
	var rates []map[string]json.RawMessage
	if err := json.Unmarshal(root["rates"], &rates); err != nil {
		t.Fatal(err)
	}
	if len(rates) == 0 {
		t.Fatal("rates JSON is empty")
	}
	for _, rate := range rates {
		if got := sortedRawKeys(rate); strings.Join(got, ",") != "code,name,nominal,unit_rate,value" {
			t.Fatalf("rate fields = %v", got)
		}
	}
}

func sortedRawKeys(values map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
