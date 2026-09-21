package mcpclient

import (
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Rule is one stable part of the tools/list correctness criterion.
type Rule struct {
	ID          string `json:"id"`
	Description string `json:"description"`
}

var Rules = []Rule{
	{ID: "R1", Description: `поле jsonrpc равно строке "2.0"`},
	{ID: "R2", Description: "id ответа равен id запроса"},
	{ID: "R3", Description: "есть result.tools и массив не пуст"},
	{ID: "R4", Description: "у каждого инструмента непустое строковое name"},
	{ID: "R5", Description: "у каждого инструмента inputSchema — JSON-объект"},
}

// Verdict records the result of all five rules in their stable order.
type Verdict struct {
	Passed bool          `json:"passed"`
	Rules  []RuleVerdict `json:"rules"`
}

type RuleVerdict struct {
	ID string `json:"id"`
	// Measured is false when the transport gave no evidence the rule could be checked
	// against. An unmeasured rule is never reported as passed: the stdio path has no
	// JSON-RPC envelope of its own, so R1 and R2 have nothing to look at there.
	Measured bool `json:"measured"`
	Passed   bool `json:"passed"`
}

// Evaluate evaluates R1-R5 on decoded JSON-RPC request and response objects.
func Evaluate(request, response map[string]any) Verdict {
	result, hasResult := response["result"].(map[string]any)
	tools, hasTools := result["tools"].([]any)
	rules := []RuleVerdict{
		{ID: "R1", Measured: true, Passed: response["jsonrpc"] == "2.0"},
		{ID: "R2", Measured: true, Passed: equalJSONValue(request["id"], response["id"])},
		{ID: "R3", Measured: true, Passed: hasResult && hasTools && len(tools) > 0},
		{ID: "R4", Measured: true, Passed: validToolNames(tools)},
		{ID: "R5", Measured: true, Passed: validInputSchemas(tools)},
	}
	return newVerdict(rules)
}

// newVerdict passes when no MEASURED rule failed. An unmeasured rule neither passes
// nor fails the verdict: it is reported separately so the claim stays as narrow as
// the evidence behind it.
func newVerdict(rules []RuleVerdict) Verdict {
	v := Verdict{Passed: true, Rules: rules}
	for _, rule := range rules {
		if rule.Measured && !rule.Passed {
			v.Passed = false
		}
	}
	return v
}

// UnmeasuredRules lists the rules the transport could not supply evidence for.
func (v Verdict) UnmeasuredRules() []string {
	var unmeasured []string
	for _, rule := range v.Rules {
		if !rule.Measured {
			unmeasured = append(unmeasured, rule.ID)
		}
	}
	return unmeasured
}

func equalJSONValue(left, right any) bool {
	l, err := json.Marshal(left)
	if err != nil {
		return false
	}
	r, err := json.Marshal(right)
	return err == nil && string(l) == string(r)
}

func validToolNames(tools []any) bool {
	for _, tool := range tools {
		object, ok := tool.(map[string]any)
		name, named := object["name"].(string)
		if !ok || !named || name == "" {
			return false
		}
	}
	return true
}

func validInputSchemas(tools []any) bool {
	for _, tool := range tools {
		object, ok := tool.(map[string]any)
		if !ok {
			return false
		}
		if _, ok := object["inputSchema"].(map[string]any); !ok {
			return false
		}
	}
	return true
}

// EvaluateToolsResult checks what the SDK-decoded response can actually show: R3-R5.
// R1 and R2 live in the JSON-RPC envelope, which the SDK has already consumed, so on a
// transport without a raw capture (stdio) they are reported UNMEASURED rather than
// passed. Wrapping the decoded body in a synthetic envelope would make both rules true
// by construction — a claim about our own scaffolding, not about the server.
func EvaluateToolsResult(result *mcp.ListToolsResult) (Verdict, error) {
	if result == nil {
		return Verdict{}, fmt.Errorf("пустой ответ tools/list")
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return Verdict{}, err
	}
	var body map[string]any
	if err := json.Unmarshal(encoded, &body); err != nil {
		return Verdict{}, err
	}
	tools, _ := body["tools"].([]any)
	return newVerdict([]RuleVerdict{
		{ID: "R1"},
		{ID: "R2"},
		{ID: "R3", Measured: true, Passed: len(tools) > 0},
		{ID: "R4", Measured: true, Passed: validToolNames(tools)},
		{ID: "R5", Measured: true, Passed: validInputSchemas(tools)},
	}), nil
}

// FailedRules gives a human-readable list of failed rule IDs.
func (v Verdict) FailedRules() []string {
	var failed []string
	for _, rule := range v.Rules {
		if !rule.Passed {
			failed = append(failed, rule.ID)
		}
	}
	return failed
}
