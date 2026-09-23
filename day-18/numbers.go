package main

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

var numberRE = regexp.MustCompile(`[-+]?(?:\d+(?:[ \x{00A0}\x{202F}]\d{3})+(?:[,.]\d+)?|\d{1,3}(?:,\d{3})+(?:\.\d+)?|\d+(?:[,.]\d+)?)`)
var commaGroups = regexp.MustCompile(`^\d{1,3}(?:,\d{3})+$`)

func numbers(text string) (out []float64) {
	for _, raw := range numberRE.FindAllString(text, -1) {
		value := strings.NewReplacer(" ", "", "\u00a0", "", "\u202f", "").Replace(raw)
		var candidates []string
		switch {
		case strings.Contains(value, ",") && strings.Contains(value, "."):
			candidates = append(candidates, strings.ReplaceAll(value, ",", ""))
		case commaGroups.MatchString(value):
			candidates = append(candidates, strings.ReplaceAll(value, ",", "."), strings.ReplaceAll(value, ",", ""))
		default:
			candidates = append(candidates, strings.ReplaceAll(value, ",", "."))
		}
		for _, candidate := range candidates {
			if number, err := strconv.ParseFloat(candidate, 64); err == nil {
				out = append(out, number)
			}
		}
	}
	return out
}

func usesValue(answer string, want float64) bool {
	for _, number := range numbers(answer) {
		if math.Abs(number-want) <= .01 {
			return true
		}
	}
	return false
}
