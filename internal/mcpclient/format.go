package mcpclient

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode"
)

// Formatted is the human-readable view plus the two byte counts it reports.
type Formatted struct {
	Text          string
	ToolsBytes    int
	ResponseBytes int
}

// Format builds the stable, human-readable tools/list presentation.
func Format(result Result) (Formatted, error) {
	if result.Tools == nil {
		return Formatted{}, fmt.Errorf("пустой ответ tools/list")
	}
	toolsJSON, err := json.Marshal(result.Tools.Tools)
	if err != nil {
		return Formatted{}, err
	}
	responseJSON, err := json.Marshal(result.Tools)
	if err != nil {
		return Formatted{}, err
	}
	responseBytes := len(responseJSON)
	if result.ResponseBytes > 0 {
		responseBytes = result.ResponseBytes
	}
	server := result.ServerName
	if result.ServerVersion != "" {
		server += " " + result.ServerVersion
	}
	if server == "" {
		server = "MCP server"
	}
	target := result.Transport.Endpoint
	if result.Transport.Command != "" {
		target = result.Transport.Command
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s · протокол %s · %s · %s\n", SafeForTerminal(server), SafeForTerminal(result.ProtocolVersion), result.Transport.Description, target)
	for i, tool := range result.Tools.Tools {
		fmt.Fprintf(&b, "%d. %s", i+1, SafeForTerminal(tool.Name))
		if description := oneLine(tool.Description); description != "" {
			fmt.Fprintf(&b, " — %s", description)
		}
		if required := requiredParams(tool.InputSchema); len(required) > 0 {
			fmt.Fprintf(&b, " (обязательные: %s)", strings.Join(required, ", "))
		}
		b.WriteByte('\n')
	}
	fmt.Fprintf(&b, "Итого: %d инструментов · tools %d байт · ответ %d байт\n", len(result.Tools.Tools), len(toolsJSON), responseBytes)
	if unmeasured := result.Verdict.UnmeasuredRules(); len(unmeasured) > 0 {
		fmt.Fprintf(&b, "Критерий: выполнены %s; %s не измерены — у этого транспорта нет сырого JSON-RPC-конверта\n",
			strings.Join(measuredRuleIDs(result.Verdict), ", "), strings.Join(unmeasured, ", "))
	} else {
		fmt.Fprintf(&b, "Критерий: выполнены все правила (%s)\n", strings.Join(measuredRuleIDs(result.Verdict), ", "))
	}
	return Formatted{Text: b.String(), ToolsBytes: len(toolsJSON), ResponseBytes: responseBytes}, nil
}

func measuredRuleIDs(v Verdict) []string {
	var measured []string
	for _, rule := range v.Rules {
		if rule.Measured {
			measured = append(measured, rule.ID)
		}
	}
	return measured
}

func oneLine(description string) string {
	description = strings.Join(strings.Fields(SafeForTerminal(description)), " ")
	const limit = 120
	if len([]rune(description)) <= limit {
		return description
	}
	return string([]rune(description)[:limit-1]) + "…"
}

// SafeForTerminal replaces control characters with U+FFFD before anything reaches a
// terminal. Exported because the CLI prints the server name a second time in its own
// summary line: a sanitizer that only one of two print sites can reach protects neither.
// Tool names and descriptions come from somebody else's server, and an ESC sequence in
// them can retitle the window, clear the screen or hide the lines above it — that is, make
// the recorded demo show something that was never run. strings.Fields does not help: ESC
// is not whitespace.
func SafeForTerminal(value string) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' {
			return ' '
		}
		// Cf covers the invisible formatting characters too, including the bidi
		// overrides that can reverse how a name reads without changing what it is.
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return '\uFFFD'
		}
		return r
	}, value)
}

func requiredParams(schema any) []string {
	object, ok := schema.(map[string]any)
	if !ok {
		return nil
	}
	required, ok := object["required"].([]any)
	if !ok {
		return nil
	}
	params := make([]string, 0, len(required))
	for _, param := range required {
		if value, ok := param.(string); ok {
			params = append(params, SafeForTerminal(value))
		}
	}
	return params
}

// CaptureDocument is the on-disk form of a raw HTTP exchange capture.
type CaptureDocument struct {
	Exchanges []Exchange `json:"exchanges,omitempty"`
	Note      string     `json:"note,omitempty"`
}

// WriteCapture serializes HTTP exchanges, or explicitly explains the stdio limitation.
func WriteCapture(w io.Writer, transport Transport) error {
	document := CaptureDocument{}
	if transport.Capture == nil {
		document.Note = "сырой обмен пишется только для HTTP-транспорта: у stdio своего HTTP-обмена нет"
	} else {
		document.Exchanges = transport.Capture.Exchanges()
		if len(document.Exchanges) == 0 {
			document.Note = "ни один HTTP-обмен не был записан"
		}
	}
	encoded := json.NewEncoder(w)
	encoded.SetIndent("", "  ")
	return encoded.Encode(document)
}
