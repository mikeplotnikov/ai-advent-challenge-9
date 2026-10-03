package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type TurnAssessment struct {
	Scenario string `json:"scenario"`
	Number   int    `json:"number"`
	Verdict  string `json:"verdict"`
	Reason   string `json:"reason"`
	Goal     string `json:"goal_adherence"`
}
type ScenarioAssessment struct {
	ID     string `json:"id"`
	Memory string `json:"memory_verdict"`
	Final  string `json:"final_goal_verdict"`
	Reason string `json:"reason"`
}
type Assessment struct {
	RunSHA    string               `json:"run_sha256"`
	Turns     []TurnAssessment     `json:"turns"`
	Scenarios []ScenarioAssessment `json:"scenarios"`
}

func readAssessment(path, sha string, r ChatRun) (*Assessment, error) {
	b, e := os.ReadFile(path)
	if os.IsNotExist(e) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	var a Assessment
	if e = json.Unmarshal(b, &a); e != nil {
		return nil, e
	}
	if a.RunSHA != sha {
		return nil, fmt.Errorf("assessment run hash mismatch")
	}
	valid := map[string]bool{}
	for _, s := range r.Scenarios {
		for _, t := range s.Session.Turns {
			valid[fmt.Sprintf("%s/%d", s.ID, t.Number)] = t.RAG.Answer.Unknown
		}
	}
	seen := map[string]bool{}
	for _, v := range a.Turns {
		k := fmt.Sprintf("%s/%d", v.Scenario, v.Number)
		unknown, ok := valid[k]
		if !ok || seen[k] || strings.TrimSpace(v.Reason) == "" {
			return nil, fmt.Errorf("invalid assessment turn")
		}
		seen[k] = true
		if !containsString([]string{"supported", "unsupported", "unknown"}, v.Verdict) || (v.Verdict == "unknown") != unknown {
			return nil, fmt.Errorf("assessment verdict mismatch")
		}
	}
	if len(seen) != len(valid) || len(a.Scenarios) != len(r.Scenarios) {
		return nil, fmt.Errorf("incomplete assessment")
	}
	ids := map[string]bool{}
	for _, s := range r.Scenarios {
		ids[s.ID] = true
	}
	for _, s := range a.Scenarios {
		if !ids[s.ID] || s.Reason == "" || !containsString([]string{"retained", "lost"}, s.Memory) || !containsString([]string{"addressed", "missed", "unclear"}, s.Final) {
			return nil, fmt.Errorf("invalid scenario assessment")
		}
		delete(ids, s.ID)
	}
	return &a, nil
}
