package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/mcpclient"
	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/ratesmcp"
)

func TestDumpMatchesTheCopyTheShowcaseChecksAgainst(t *testing.T) {
	const copyPath = "../../uchebnik-ai-advent/challeng/test/day17-definitions.json"
	committed, err := os.ReadFile(copyPath)
	if err != nil {
		t.Skipf("копии витрины нет рядом (%v)", err)
	}
	var got bytes.Buffer
	if err := writeDump(&got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimSpace(committed), bytes.TrimSpace(got.Bytes())) {
		t.Errorf("выгрузка разошлась с копией витрины. Обнови её: go run ./day-17 -dump > %s", copyPath)
	}
}

func TestDumpIsDeterministic(t *testing.T) {
	var first, second bytes.Buffer
	if err := writeDump(&first); err != nil {
		t.Fatal(err)
	}
	if err := writeDump(&second); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("writeDump produced different bytes")
	}
}

func TestDumpContainsTheReplayContract(t *testing.T) {
	var out bytes.Buffer
	if err := writeDump(&out); err != nil {
		t.Fatal(err)
	}
	var dump dumpDefinitions
	if err := json.Unmarshal(out.Bytes(), &dump); err != nil {
		t.Fatal(err)
	}
	if dump.SystemPrompt != systemPrompt || dump.MaxModelCalls != 5 || dump.Temperature != 0 || dump.Model == "" {
		t.Fatalf("agent definition is incomplete: %#v", dump)
	}
	if _, ok := dump.Pricing["deepseek-flash"]; !ok {
		t.Fatal("flash pricing missing")
	}
	if _, ok := dump.Pricing["deepseek-v4-flash"]; !ok {
		t.Fatal("legacy flash pricing missing")
	}
	want := map[string]bool{"rates-usd-kzt": true, "rates-empty-codes": true, "rates-lowercase": true, "rates-unknown": true, "rates-weekend": true, "rates-latest": true, "date-tomorrow": true, "date-future": true, "date-before-min": true, "date-invalid": true, "convert-usd-rub": true, "convert-cny-kzt": true, "convert-lowercase": true, "convert-zero": true, "convert-same": true, "convert-unknown-code": true, "empty": true, "error-in-parameters": true, "http500": true, "network": true, "schema-amount": true, "schema-extra": true}
	specs := map[string]dumpSpec{}
	for _, spec := range dumpSpecs() {
		specs[spec.name] = spec
	}
	for _, item := range dump.Cases {
		if !want[item.Name] {
			t.Errorf("unexpected case %q", item.Name)
		}
		delete(want, item.Name)
		if item.Upstream == "xml" && (item.Fixture == nil || item.FixtureBase64 == nil) {
			t.Errorf("xml case %q has no fixture bytes", item.Name)
		}
		if item.Upstream != "xml" && (item.Fixture != nil || item.FixtureBase64 != nil) {
			t.Errorf("non-xml case %q has fixture bytes", item.Name)
		}
		if strings.HasPrefix(item.Name, "schema-") && item.Compare != "isError" {
			t.Errorf("%s compare=%q", item.Name, item.Compare)
		}
		spec, ok := specs[item.Name]
		if !ok {
			continue
		}
		itemArgs, _ := json.Marshal(item.Arguments)
		specArgs, _ := json.Marshal(spec.args)
		if item.Tool != spec.tool || item.Compare != spec.compare || !bytes.Equal(itemArgs, specArgs) {
			t.Errorf("case %q contract differs from spec", item.Name)
		}
		if item.Upstream == "xml" {
			raw, err := ratesmcp.Fixtures.ReadFile("fixtures/" + spec.fixture)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := base64.StdEncoding.DecodeString(*item.FixtureBase64)
			if err != nil || !bytes.Equal(raw, decoded) {
				t.Errorf("case %q fixtureBase64 differs from fixture", item.Name)
			}
		}
		result, err := runDumpCase(spec)
		if err != nil {
			t.Fatal(err)
		}
		expected := dumpExpected{IsError: result.IsError, Text: mcpclient.ToolText(result), Structured: result.StructuredContent}
		gotJSON, _ := json.Marshal(item.Expected)
		wantJSON, _ := json.Marshal(expected)
		if !bytes.Equal(gotJSON, wantJSON) {
			t.Errorf("case %q expected differs from real server\ngot:  %s\nwant: %s", item.Name, gotJSON, wantJSON)
		}
	}
	for name := range want {
		t.Errorf("required case %q missing", name)
	}
}

func TestPeakCopyMatchesLLMForAWeek(t *testing.T) {
	var out bytes.Buffer
	if err := writeDump(&out); err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Peak struct {
			WeekdaysOnly bool    `json:"weekdaysOnly"`
			Hours        [][]int `json:"hoursUTC"`
		} `json:"peak"`
	}
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	for hour := 0; hour < 24*7; hour++ {
		at := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC).Add(time.Duration(hour) * time.Hour)
		copyPeak := decoded.Peak.WeekdaysOnly && at.Weekday() != time.Saturday && at.Weekday() != time.Sunday && inDumpPeakHour(at.Hour(), decoded.Peak.Hours)
		if copyPeak != llm.IsPeak(at) {
			t.Fatalf("%s: copy=%v llm=%v", at, copyPeak, llm.IsPeak(at))
		}
	}
}
func inDumpPeakHour(hour int, ranges [][]int) bool {
	for _, r := range ranges {
		if len(r) == 2 && hour >= r[0] && hour < r[1] {
			return true
		}
	}
	return false
}
