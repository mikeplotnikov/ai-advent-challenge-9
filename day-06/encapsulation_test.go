package main

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The task's own words: "агент должен быть отдельной сущностью, а не просто один
// вызов API; логика запроса и ответа должна быть инкапсулирована в агенте".
//
// That claim is cheap to write in a report and cheap to break in code, so it is
// checked instead: this package — the interface — must not reach the transport.
// If day-06 ever imports internal/llm, net/http or encoding/json again, the request
// logic has leaked back out of the agent and this test says so.
func TestInterfaceDoesNotReachTheTransport(t *testing.T) {
	forbidden := map[string]string{
		"github.com/mikeplotnikov/ai-advent-challenge-9/internal/llm": "клиент модели — логика запроса должна жить в агенте",
		"net/http":      "HTTP — интерфейс не должен знать, что модель вызывается по сети",
		"encoding/json": "разбор ответа модели — это работа агента",
	}

	for _, file := range goFiles(t, ".") {
		fset := token.NewFileSet()
		parsed, err := parser.ParseFile(fset, file, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		for _, imp := range parsed.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatalf("%s: импорт %s: %v", file, imp.Path.Value, err)
			}
			if why, bad := forbidden[path]; bad {
				t.Errorf("%s импортирует %q — %s", file, path, why)
			}
		}
	}
}

// The other half of the same claim: the interface must actually go through the agent
// package. A CLI that imports nothing at all would pass the test above trivially.
func TestInterfaceGoesThroughTheAgentPackage(t *testing.T) {
	const want = "github.com/mikeplotnikov/ai-advent-challenge-9/internal/agent"
	for _, file := range goFiles(t, ".") {
		fset := token.NewFileSet()
		parsed, err := parser.ParseFile(fset, file, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		for _, imp := range parsed.Imports {
			if path, _ := strconv.Unquote(imp.Path.Value); path == want {
				return
			}
		}
	}
	t.Fatalf("ни один файл day-06 не импортирует %q — интерфейс не ходит через агента", want)
}

// The interface must not build the request either: no message roles, no API field
// names. Those strings appearing here would mean the stack is assembled outside the
// agent, which is exactly what the host said the agent has to take on itself.
func TestInterfaceDoesNotSpellOutTheWireFormat(t *testing.T) {
	forbidden := []string{
		`"assistant"`, `"messages"`, `"choices"`,
		"api.deepseek.com", "DEEPSEEK_API_KEY", "Bearer ",
	}
	for _, file := range goFiles(t, ".") {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		for _, bad := range forbidden {
			if strings.Contains(string(src), bad) {
				t.Errorf("%s содержит %s — формат запроса собирается вне агента", file, bad)
			}
		}
	}
}

func goFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("чтение %s: %v", dir, err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		out = append(out, filepath.Join(dir, e.Name()))
	}
	if len(out) == 0 {
		t.Fatal("в day-06 не найдено ни одного .go файла — проверка ничего не проверяет")
	}
	return out
}
