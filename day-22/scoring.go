package main

import (
	"regexp"
	"strings"
)

const noDataMarker = "[[НЕТ ДАННЫХ]]"

var citationRE = regexp.MustCompile(`\[источник:\s*([^\]]+)\]`)

func scoreAnswer(question Question, text string, chunks []FoundChunk, empty bool) Score {
	if empty {
		return Score{Outcome: "empty"}
	}
	score := Score{Marker: strings.Contains(text, noDataMarker), AllCitationsFound: true}
	for _, fact := range question.Facts {
		for _, pattern := range fact.Patterns {
			re, _ := compilePattern(pattern)
			if match := re.FindString(text); match != "" {
				score.MatchedFacts = append(score.MatchedFacts, MatchedFact{Name: fact.Name, Match: match})
				break
			}
		}
	}
	foundSources := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		foundSources = append(foundSources, chunk.Source)
	}
	for _, match := range citationRE.FindAllStringSubmatch(text, -1) {
		source := strings.TrimSpace(match[1])
		citation := Citation{Source: source, Found: containsString(foundSources, source), Expected: containsString(question.Sources, source)}
		score.Citations = append(score.Citations, citation)
		if !citation.Found {
			score.AllCitationsFound = false
		}
		if citation.Expected {
			score.ExpectedCitation = true
		}
	}
	score.MarkerWithFacts = score.Marker && len(score.MatchedFacts) > 0
	score.Uncited = len(score.MatchedFacts) > 0 && len(score.Citations) == 0
	if question.Kind == "out_of_base" {
		neighbour := false
		for _, pattern := range question.Neighbours {
			re, _ := compilePattern(pattern)
			if re.MatchString(text) {
				neighbour = true
				break
			}
		}
		if score.Marker && !neighbour {
			score.Outcome, score.Success = "declined", true
		} else {
			score.Outcome = "fabricated"
		}
		return score
	}
	matched := len(score.MatchedFacts)
	switch {
	case matched == len(question.Facts) && matched > 0:
		score.Outcome, score.Success = "correct", true
	case matched > 0:
		score.Outcome = "partial"
	case score.Marker:
		score.Outcome = "unknown"
	default:
		score.Outcome = "wrong"
	}
	return score
}

func majority(calls []Call) bool {
	successes := 0
	for _, call := range calls {
		if call.Score.Success {
			successes++
		}
	}
	return successes >= 2
}
