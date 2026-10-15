package main

import (
	"bytes"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"unicode/utf8"
)

// change is one file the pull request touches.
type change struct {
	Status  string // A, M, D, R, C, T as git prints it, without the similarity score
	Path    string // path at head; the old path for a deleted file
	OldPath string // set for renames and copies
	Binary  bool
	Diff    string // annotated hunks, see annotate
	Lines   int    // line count of the file at head, 0 when deleted or binary
	Content string // the file at head with line numbers, empty when skipped
}

// pullRequest is what the reviewer gets from git: the changed files with their diffs
// and contents, plus notes about everything left out of the prompt.
type pullRequest struct {
	Base, Head string
	Changes    []change
	Omitted    []string
}

const (
	maxDiffRunes    = 60000
	maxContentRunes = 60000
	maxFileRunes    = 20000
)

// git runs with core.quotepath off, so a non-ASCII path reads the same in the patch as
// in --name-status -z; otherwise its hunks could not be matched to the file.
func git(repo string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", repo, "-c", "core.quotepath=false"}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

// collect reads the pull request as base...head: the changes on head since the point it
// branched from base, which is what GitHub shows on the PR's "Files changed" tab.
func collect(repo, base, head string) (pullRequest, error) {
	pr := pullRequest{}
	for _, ref := range []*string{&base, &head} {
		sha, err := git(repo, "rev-parse", "--verify", "--end-of-options", *ref+"^{commit}")
		if err != nil {
			return pr, err
		}
		*ref = strings.TrimSpace(sha)
	}
	pr.Base, pr.Head = base, head
	rng := base + "..." + head
	status, err := git(repo, "diff", "--name-status", "-z", "-M", rng)
	if err != nil {
		return pr, err
	}
	numstat, err := git(repo, "diff", "--numstat", "-z", "-M", rng)
	if err != nil {
		return pr, err
	}
	binary := binaryPaths(numstat)
	pr.Changes = parseNameStatus(status)
	if len(pr.Changes) == 0 {
		return pr, fmt.Errorf("между %s и %s нет изменений", base, head)
	}
	patch, err := git(repo, "diff", "--no-color", "--no-ext-diff", "--unified=3", "-M", rng)
	if err != nil {
		return pr, err
	}
	hunks := splitPatch(patch)
	diffBudget, contentBudget := maxDiffRunes, maxContentRunes
	for i := range pr.Changes {
		c := &pr.Changes[i]
		c.Binary = binary[c.Path]
		if c.Binary {
			pr.Omitted = append(pr.Omitted, c.Path+": двоичный файл")
			continue
		}
		c.Diff = annotate(hunks[c.Path])
		if c.Diff == "" && (c.Status == "M" || c.Status == "A") {
			pr.Omitted = append(pr.Omitted, c.Path+": diff не разобран — модель видит только файл целиком")
		}
		if n := utf8.RuneCountInString(c.Diff); n > diffBudget {
			pr.Omitted = append(pr.Omitted, fmt.Sprintf("%s: diff не передан модели — превышен общий лимит %d символов", c.Path, maxDiffRunes))
			c.Diff = ""
		} else {
			diffBudget -= n
		}
		if c.Status == "D" {
			continue
		}
		text, err := git(repo, "show", head+":"+c.Path)
		if err != nil {
			return pr, err
		}
		c.Lines = strings.Count(text, "\n")
		if text != "" && !strings.HasSuffix(text, "\n") {
			c.Lines++
		}
		n := utf8.RuneCountInString(text)
		switch {
		case n > maxFileRunes:
			pr.Omitted = append(pr.Omitted, fmt.Sprintf("%s: файл целиком не передан — длиннее %d символов, модель видит только diff", c.Path, maxFileRunes))
		case n > contentBudget:
			pr.Omitted = append(pr.Omitted, fmt.Sprintf("%s: файл целиком не передан — превышен общий лимит %d символов", c.Path, maxContentRunes))
		default:
			c.Content = numberLines(text)
			contentBudget -= n
		}
	}
	return pr, nil
}

// parseNameStatus reads `git diff --name-status -z`: a status field, then one path, or
// two (old, new) for renames and copies.
func parseNameStatus(out string) []change {
	fields := strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
	var changes []change
	for i := 0; i < len(fields); {
		status := fields[i]
		if status == "" {
			i++
			continue
		}
		kind := status[:1]
		if (kind == "R" || kind == "C") && i+2 < len(fields) {
			changes = append(changes, change{Status: kind, OldPath: fields[i+1], Path: fields[i+2]})
			i += 3
			continue
		}
		if i+1 >= len(fields) {
			break
		}
		changes = append(changes, change{Status: kind, Path: fields[i+1]})
		i += 2
	}
	return changes
}

// binaryPaths reads `git diff --numstat -z`, where a binary file shows "-" for both
// counts. A rename is "added\tdeleted\t" followed by two NUL-terminated paths.
func binaryPaths(out string) map[string]bool {
	binary := map[string]bool{}
	fields := strings.Split(out, "\x00")
	for i := 0; i < len(fields); i++ {
		parts := strings.SplitN(fields[i], "\t", 3)
		if len(parts) != 3 {
			continue
		}
		path := parts[2]
		if path == "" && i+2 < len(fields) {
			path = fields[i+2]
			i += 2
		}
		if parts[0] == "-" && parts[1] == "-" {
			binary[path] = true
		}
	}
	return binary
}

// splitPatch cuts a unified diff into the hunks of each file, keyed by the path at head
// (the old path for a deletion).
func splitPatch(patch string) map[string]string {
	out := map[string]string{}
	var path string
	var body strings.Builder
	flush := func() {
		if path != "" {
			out[path] = body.String()
		}
		body.Reset()
	}
	var oldPath string
	inHunks := false
	for _, line := range strings.SplitAfter(patch, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			flush()
			path, oldPath, inHunks = "", "", false
		case !inHunks && strings.HasPrefix(line, "--- "):
			oldPath = strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(line, "--- ")), "a/")
		case !inHunks && strings.HasPrefix(line, "+++ "):
			newPath := strings.TrimSpace(strings.TrimPrefix(line, "+++ "))
			if newPath == "/dev/null" {
				path = oldPath
			} else {
				path = strings.TrimPrefix(newPath, "b/")
			}
		case strings.HasPrefix(line, "@@"):
			inHunks = true
			body.WriteString(line)
		case inHunks:
			body.WriteString(line)
		}
	}
	flush()
	return out
}

// annotate prefixes every line of the hunks with its number in the new file, so the
// model cites lines by reading them instead of counting from the hunk header.
// Removed lines get no number: they do not exist at head.
func annotate(hunks string) string {
	var out strings.Builder
	next := 0
	for _, line := range strings.SplitAfter(hunks, "\n") {
		if line == "" {
			continue
		}
		switch line[0] {
		case '@':
			next = hunkStart(line)
			out.WriteString(line)
		case '+', ' ':
			fmt.Fprintf(&out, "%5d %s", next, line)
			next++
		case '-':
			fmt.Fprintf(&out, "%5s %s", "", line)
		default: // "\ No newline at end of file"
			fmt.Fprintf(&out, "%5s %s", "", line)
		}
		if !strings.HasSuffix(line, "\n") {
			out.WriteString("\n")
		}
	}
	return out.String()
}

// hunkStart reads the new-file start line from "@@ -a,b +c,d @@".
func hunkStart(header string) int {
	plus := strings.Index(header, " +")
	if plus < 0 {
		return 0
	}
	rest := header[plus+2:]
	end := strings.IndexAny(rest, ", ")
	if end < 0 {
		return 0
	}
	n, err := strconv.Atoi(rest[:end])
	if err != nil {
		return 0
	}
	return n
}

func numberLines(text string) string {
	var out strings.Builder
	for i, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		fmt.Fprintf(&out, "%5d| %s\n", i+1, line)
	}
	return out.String()
}

func (pr pullRequest) changed(path string) (change, bool) {
	for _, c := range pr.Changes {
		if c.Path == path {
			return c, true
		}
	}
	return change{}, false
}
