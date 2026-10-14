package main

import (
	"bufio"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

// demoInput feeds the REPL from a script for a silent screencast: a line starting
// with "#" is a caption printed on screen, any other line is typed at the prompt.
// The REPL reads it like a keyboard, so the recording shows the real code path.
type demoInput struct {
	lines []string
	out   io.Writer
	pace  float64
	next  int
	buf   strings.Reader
}

func newDemoInput(path string, out io.Writer) (*demoInput, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	pace := 1.0
	if value, err := strconv.ParseFloat(os.Getenv("PACE"), 64); err == nil && value > 0 {
		pace = value
	}
	var lines []string
	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	for scanner.Scan() {
		if line := strings.TrimRight(scanner.Text(), " \t"); line != "" {
			lines = append(lines, line)
		}
	}
	return &demoInput{lines: lines, out: out, pace: pace}, nil
}

// Read is called by the REPL's scanner only after the previous command has finished,
// so each caption and command appears after the answer above it is on screen.
func (d *demoInput) Read(p []byte) (int, error) {
	if d.buf.Len() > 0 {
		return d.buf.Read(p)
	}
	for d.next < len(d.lines) {
		line := d.lines[d.next]
		d.next++
		if caption, ok := strings.CutPrefix(line, "#"); ok {
			d.pause(1.2)
			// The REPL has already printed its prompt: erase it, print the caption on
			// its own line, then give the prompt back for the command that follows.
			io.WriteString(d.out, "\r\033[2K\033[2m# "+strings.TrimSpace(caption)+"\033[0m\n")
			d.pause(2.4)
			io.WriteString(d.out, prompt)
			continue
		}
		d.pause(1.0)
		for _, r := range line {
			io.WriteString(d.out, string(r))
			d.pause(0.035)
		}
		io.WriteString(d.out, "\n")
		d.pause(0.6)
		d.buf.Reset(line + "\n")
		return d.buf.Read(p)
	}
	d.pause(2)
	return 0, io.EOF
}

func (d *demoInput) pause(seconds float64) {
	time.Sleep(time.Duration(seconds * d.pace * float64(time.Second)))
}
