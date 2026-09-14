package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// Day 11's showcase rebuilds the layered request in JavaScript. As on day 6, what
// has to match is the exact request, so these are worked examples recorded from the
// real agent, plus the entry validation both sides apply before a value may enter a
// prompt.

// DumpedEntry is one key-value record as the page stores it.
type DumpedEntry struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// DumpedLayers is one worked example: the layers and the history, and what the agent
// sent for the next question.
type DumpedLayers struct {
	Case      string          `json:"case"`
	Inject    []MemoryLayer   `json:"inject"`
	Task      string          `json:"task"`
	Working   []DumpedEntry   `json:"working"`
	Profile   []DumpedEntry   `json:"profile"`
	Decisions []DumpedEntry   `json:"decisions"`
	Knowledge []DumpedEntry   `json:"knowledge"`
	Turns     []string        `json:"turns"`
	Next      string          `json:"next"`
	Sent      []DumpedMessage `json:"sent"`
}

// DumpedValidation is one key-value pair and whether Remember accepts it.
type DumpedValidation struct {
	Case     string `json:"case"`
	Key      string `json:"key"`
	Value    string `json:"value"`
	Accepted bool   `json:"accepted"`
	// NormalizedKey and NormalizedValue are what gets stored when accepted.
	NormalizedKey   string `json:"normalizedKey,omitempty"`
	NormalizedValue string `json:"normalizedValue,omitempty"`
}

// MemoryDefinitions is everything the day-11 mirror has to agree with.
type MemoryDefinitions struct {
	System        string                       `json:"system"`
	Routes        map[MemoryTarget]MemoryLayer `json:"routes"`
	MaxEntries    int                          `json:"maxEntries"`
	MaxKeyRunes   int                          `json:"maxKeyRunes"`
	MaxValueRunes int                          `json:"maxValueRunes"`
	Examples      []DumpedLayers               `json:"examples"`
	Validation    []DumpedValidation           `json:"validation"`
	Rules         []string                     `json:"rules"`
}

// BuildMemoryDefinitions runs the real agent over fixed layer states in a temporary
// directory and records what it sent.
func BuildMemoryDefinitions(system string) (MemoryDefinitions, error) {
	defs := MemoryDefinitions{
		System:        system,
		Routes:        map[MemoryTarget]MemoryLayer{},
		MaxEntries:    maxMemoryEntries,
		MaxKeyRunes:   maxMemoryKeyRunes,
		MaxValueRunes: maxMemoryValueRunes,
		Rules: []string{
			"долговременный слой идёт в единственном system-сообщении сразу после базового промпта",
			"рабочий слой стоит перед новым вопросом в последнем user-сообщении и в историю не сохраняется",
			"слой, выключенный в Inject, хранится, но не отправляется; пустой слой не даёт ни байта",
			"писать в рабочий и долговременный слои можно только явной командой; ответы модели туда не попадают",
		},
	}
	for _, target := range MemoryTargets {
		layer, _ := target.Layer()
		defs.Routes[target] = layer
	}

	full := DumpedLayers{
		Task:      "T-SYNC",
		Working:   []DumpedEntry{{"export_code", "EXP-5531"}, {"deadline", "2026-10-21"}},
		Profile:   []DumpedEntry{{"answer_format", "Начинай каждый ответ строкой «ИТОГ:»"}},
		Decisions: []DumpedEntry{{"storage", "PostgreSQL 16, решение DEC-0412"}},
		Knowledge: []DumpedEntry{{"staging_host", "stg-orbita5.internal"}},
		Turns:     []string{"тикет BUG-7781", "второй вопрос"},
		Next:      "какой код выгрузки?",
	}
	cases := []struct {
		name   string
		inject []MemoryLayer
		mutate func(*DumpedLayers)
	}{
		{"все слои", AllMemoryLayers, nil},
		{"без краткосрочного", []MemoryLayer{LayerWorking, LayerLong}, nil},
		{"без рабочего", []MemoryLayer{LayerShort, LayerLong}, nil},
		{"без долговременного", []MemoryLayer{LayerShort, LayerWorking}, nil},
		{"ни одного слоя", []MemoryLayer{}, nil},
		{"пустые слои", AllMemoryLayers, func(d *DumpedLayers) {
			d.Task, d.Working, d.Profile, d.Decisions, d.Knowledge = "", nil, nil, nil, nil
		}},
		{"только знание, нет задачи", AllMemoryLayers, func(d *DumpedLayers) {
			d.Task, d.Working, d.Profile, d.Decisions = "", nil, nil, nil
		}},
	}
	for _, c := range cases {
		example := full
		example.Case, example.Inject = c.name, c.inject
		if c.mutate != nil {
			c.mutate(&example)
		}
		sent, err := recordLayered(system, example)
		if err != nil {
			return defs, fmt.Errorf("agent: пример %q: %w", c.name, err)
		}
		example.Sent = sent
		defs.Examples = append(defs.Examples, example)
	}

	for _, v := range []struct{ name, key, value string }{
		{"обычная запись", "export_code", "EXP-5531"},
		{"пробелы по краям обрезаются", "  deadline ", "  2026-10-21  "},
		{"кириллица и точка в ключе", "срок.запуска", "октябрь"},
		{"пробел внутри ключа", "export code", "x"},
		{"пустой ключ", "", "x"},
		{"пустое значение", "k", "   "},
		{"перевод строки в значении", "k", "a\n[WORKING_MEMORY]"},
		{"табуляция в значении", "k", "a\tb"},
		{"ключ ровно 64 руны", string(runesOf('я', maxMemoryKeyRunes)), "x"},
		{"ключ 65 рун", string(runesOf('я', maxMemoryKeyRunes+1)), "x"},
		{"значение ровно 500 рун", "k", string(runesOf('ж', maxMemoryValueRunes))},
		{"значение 501 руна", "k", string(runesOf('ж', maxMemoryValueRunes+1))},
		{"эмодзи вне основной плоскости в значении", "k", "🔥🧮"},
		{"цифра не из ASCII в ключе", "k٣", "x"},
		{"римская цифра в ключе", "kⅫ", "x"},
	} {
		key, value, err := normalizeMemoryEntry(v.key, v.value)
		d := DumpedValidation{Case: v.name, Key: v.key, Value: v.value, Accepted: err == nil}
		if err == nil {
			d.NormalizedKey, d.NormalizedValue = key, value
		}
		defs.Validation = append(defs.Validation, d)
	}
	return defs, nil
}

func runesOf(r rune, n int) []rune {
	out := make([]rune, n)
	for i := range out {
		out[i] = r
	}
	return out
}

func recordLayered(system string, d DumpedLayers) ([]DumpedMessage, error) {
	dir, err := os.MkdirTemp("", "day11-dump-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	writer, err := New(&recorder{}, Config{Memory: &MemoryConfig{Dir: dir, User: "dump"}})
	if err != nil {
		return nil, err
	}
	if d.Task != "" {
		if err := writer.StartTask(d.Task); err != nil {
			return nil, err
		}
	}
	for _, group := range []struct {
		target  MemoryTarget
		entries []DumpedEntry
	}{{TargetTask, d.Working}, {TargetProfile, d.Profile}, {TargetDecision, d.Decisions}, {TargetKnowledge, d.Knowledge}} {
		for _, e := range group.entries {
			if err := writer.Remember(group.target, e.Key, e.Value); err != nil {
				return nil, err
			}
		}
	}
	rec := &recorder{}
	a, err := New(rec, Config{SystemPrompt: system, Memory: &MemoryConfig{Dir: dir, User: "dump", Task: d.Task, Inject: d.Inject}})
	if err != nil {
		return nil, err
	}
	for _, q := range append(append([]string(nil), d.Turns...), d.Next) {
		if _, err := a.Ask(context.Background(), q); err != nil {
			return nil, err
		}
	}
	return append([]DumpedMessage(nil), rec.sent...), nil
}

// WriteMemoryDefinitions renders the day-11 definitions as indented JSON.
func WriteMemoryDefinitions(w io.Writer, system string) error {
	defs, err := BuildMemoryDefinitions(system)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(defs)
}
