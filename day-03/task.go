package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// The day needs one task whose answer can be checked by machine: "which method
// was most accurate" is a measurement, not an opinion. A counting problem gives
// exactly that — one integer, and the truth computed here rather than typed in.
// The wording the model reads and the loop that computes the truth are built
// from the same four constants. Written separately they could drift — raise the
// bound in the text, forget it in the loop, and the model gets scored against a
// different problem than the one it was asked.
const (
	rangeEnd       = 400
	divisibleBy    = 4
	digitSumBy     = 3
	forbiddenDigit = '8'
)

var taskText = fmt.Sprintf(
	"Сколько существует целых чисел от 1 до %d включительно, "+
		"которые делятся на %d, сумма цифр которых делится на %d "+
		"и в записи которых нет цифры %c?",
	rangeEnd, divisibleBy, digitSumBy, forbiddenDigit)

// groundTruth counts them by brute force. The "no digit 8" condition is what
// makes the task resist arithmetic: inclusion-exclusion covers the divisors but
// says nothing about digits, so the model has to enumerate — and that is exactly
// where it slips. Calibration on 2026-09-02: the plain method got this right in
// half the runs, step-by-step in five out of six.
func groundTruth() int {
	return len(groundTruthSet())
}

// groundTruthSet returns the numbers themselves, not just how many. Printing them
// is what turns "эталон 24" from a claim into something the viewer can check.
func groundTruthSet() []int {
	var found []int
	for n := 1; n <= rangeEnd; n++ {
		if n%divisibleBy != 0 || digitSum(n)%digitSumBy != 0 ||
			strings.ContainsRune(strconv.Itoa(n), forbiddenDigit) {
			continue
		}
		found = append(found, n)
	}
	return found
}

func digitSum(n int) int {
	sum := 0
	for ; n > 0; n /= 10 {
		sum += n % 10
	}
	return sum
}

// Models decorate the line they were told to write: "**ANSWER:** 24" and
// "ANSWER: **24**" are both common, and a strict pattern would score a correct
// answer as no answer at all — deflating a method's accuracy for a reason that
// has nothing to do with reasoning.
var answerLine = regexp.MustCompile(`(?i)\*{0,2}ANSWER\*{0,2}\s*:\s*\*{0,2}\s*(-?\d+)`)

// parseAnswer takes the last ANSWER line, not the first: a method that reasons
// out loud writes intermediate ones, and the line it ends on is the one it
// commits to.
func parseAnswer(text string) (int, bool) {
	found := answerLine.FindAllStringSubmatch(text, -1)
	if len(found) == 0 {
		return 0, false
	}
	n, err := strconv.Atoi(found[len(found)-1][1])
	if err != nil {
		return 0, false
	}
	return n, true
}
