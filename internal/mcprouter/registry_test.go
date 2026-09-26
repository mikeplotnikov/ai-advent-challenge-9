package mcprouter

import (
	"strings"
	"testing"
)

func TestRegistryPreservesFileOrder(t *testing.T) {
	for _, aliases := range [][]string{{"z", "a"}, {"a", "z"}} {
		raw := `{"mcpServers":{"` + aliases[0] + `":{"command":"one"},"` + aliases[1] + `":{"command":"two"}}}`
		registry, err := ParseRegistry([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		if registry.Servers[0].Alias != aliases[0] || registry.Servers[1].Alias != aliases[1] {
			t.Fatalf("order=%v registry=%#v", aliases, registry.Servers)
		}
	}
}

func TestRegistryRejectsEveryDeclaredStartupShapeError(t *testing.T) {
	tests := []struct{ name, raw, contains string }{
		{"empty", `{"mcpServers":{}}`, "пуст"},
		{"duplicate", `{"mcpServers":{"a":{"command":"x"},"a":{"command":"y"}}}`, "дублирующийся"},
		{"alias", `{"mcpServers":{"A":{"command":"x"}}}`, "недопустимый алиас"},
		{"unknown", `{"mcpServers":{"a":{"command":"x","wat":1}}}`, "unknown field"},
		{"command", `{"mcpServers":{"a":{"args":[]}}}`, "отсутствует command"},
		{"url", `{"mcpServers":{"a":{"command":"x","url":"https://example.test"}}}`, "url-серверы не поддерживаются"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseRegistry([]byte(test.raw))
			if err == nil || !strings.Contains(err.Error(), test.contains) {
				t.Fatalf("err=%v, want %q", err, test.contains)
			}
		})
	}
}
