package main

import (
	"regexp"
	"strconv"
	"strings"
)

// The day needs one task whose answer can be checked by machine: "which method
// was most accurate" is a measurement, not an opinion. A counting problem gives
// exactly that — one integer, and the truth computed here rather than typed in.
const taskText = "Сколько существует целых чисел от 1 до 400 включительно, " +
	"которые делятся на 4, сумма цифр которых делится на 3 " +
	"и в записи которых нет цифры 8?"

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
	for n := 1; n <= 400; n++ {
		if n%4 != 0 || digitSum(n)%3 != 0 || strings.ContainsRune(strconv.Itoa(n), '8') {
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

var answerLine = regexp.MustCompile(`ANSWER:\s*(-?\d+)`)

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
