// Package mcprouter loads ordered stdio MCP registries and routes namespaced tools.
package mcprouter

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
)

var aliasRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

type Entry struct {
	Alias   string            `json:"alias"`
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

type Registry struct {
	Servers []Entry `json:"mcpServers"`
}

type rawEntry struct {
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	URL     json.RawMessage   `json:"url"`
}

func LoadRegistry(path string) (Registry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Registry{}, fmt.Errorf("реестр %s: %w", path, err)
	}
	return ParseRegistry(raw)
}

func ParseRegistry(raw []byte) (Registry, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return Registry{}, fmt.Errorf("реестр: ожидается объект mcpServers")
	}
	var result Registry
	found := false
	seen := map[string]bool{}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return Registry{}, fmt.Errorf("реестр: %w", err)
		}
		key := keyToken.(string)
		if key != "mcpServers" {
			return Registry{}, fmt.Errorf("реестр: неизвестное поле %q", key)
		}
		if found {
			return Registry{}, fmt.Errorf("реестр: поле mcpServers повторяется")
		}
		found = true
		open, err := decoder.Token()
		if err != nil || open != json.Delim('{') {
			return Registry{}, fmt.Errorf("реестр: mcpServers должен быть объектом")
		}
		for decoder.More() {
			aliasToken, err := decoder.Token()
			if err != nil {
				return Registry{}, fmt.Errorf("реестр: %w", err)
			}
			alias := aliasToken.(string)
			if seen[alias] {
				return Registry{}, fmt.Errorf("сервер %q: дублирующийся алиас", alias)
			}
			seen[alias] = true
			if !aliasRE.MatchString(alias) {
				return Registry{}, fmt.Errorf("сервер %q: недопустимый алиас", alias)
			}
			var encoded json.RawMessage
			if err := decoder.Decode(&encoded); err != nil {
				return Registry{}, fmt.Errorf("сервер %q: %w", alias, err)
			}
			strict := json.NewDecoder(bytes.NewReader(encoded))
			strict.DisallowUnknownFields()
			var item rawEntry
			if err := strict.Decode(&item); err != nil {
				return Registry{}, fmt.Errorf("сервер %q: %w", alias, err)
			}
			if item.URL != nil {
				return Registry{}, fmt.Errorf("сервер %q: url-серверы не поддерживаются", alias)
			}
			if item.Command == "" {
				return Registry{}, fmt.Errorf("сервер %q: отсутствует command", alias)
			}
			result.Servers = append(result.Servers, Entry{Alias: alias, Command: item.Command, Args: item.Args, Env: item.Env})
		}
		if _, err := decoder.Token(); err != nil {
			return Registry{}, fmt.Errorf("реестр: %w", err)
		}
	}
	if _, err := decoder.Token(); err != nil {
		return Registry{}, fmt.Errorf("реестр: %w", err)
	}
	if !found || len(result.Servers) == 0 {
		return Registry{}, fmt.Errorf("реестр mcpServers пуст")
	}
	if token, err := decoder.Token(); err != io.EOF {
		return Registry{}, fmt.Errorf("реестр: лишние данные после объекта: %v", token)
	}
	return result, nil
}
