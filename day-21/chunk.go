package main

import (
	"fmt"
	"strings"
	"unicode"
)

const (
	fixedSize        = 1800
	fixedOverlap     = 180
	fixedRewind      = 100
	structureMaxSize = 3600
)

type Chunk struct {
	ChunkID   string    `json:"chunk_id"`
	Strategy  string    `json:"strategy"`
	Source    string    `json:"source"`
	Title     string    `json:"title"`
	Section   string    `json:"section"`
	Spans     int       `json:"spans"`
	Part      int       `json:"part"`
	Start     int       `json:"start"`
	End       int       `json:"end"`
	Runes     int       `json:"runes"`
	Tokens    int       `json:"tokens"`
	Text      string    `json:"text"`
	Embedding []float64 `json:"embedding"`
}

type sectionRange struct {
	Start, End       int
	HeadingStart     int
	HeadingEnd       int
	Level            int
	Heading, Section string
}

func parseSections(text []rune) []sectionRange {
	type heading struct {
		start, end, level int
		text              string
	}
	var headings []heading
	inFence := false
	fenceMarker := rune(0)
	fenceWidth := 0
	for start := 0; start < len(text); {
		end := start
		for end < len(text) && text[end] != '\n' {
			end++
		}
		lineEnd := end
		if end < len(text) {
			end++
		}
		line := text[start:lineEnd]
		trimmed := line
		spaces := 0
		for spaces < len(trimmed) && spaces < 3 && trimmed[spaces] == ' ' {
			spaces++
		}
		trimmed = trimmed[spaces:]
		if marker, width, ok := fence(trimmed); ok {
			if !inFence {
				inFence, fenceMarker, fenceWidth = true, marker, width
			} else if marker == fenceMarker && width >= fenceWidth {
				inFence = false
			}
			start = end
			continue
		}
		if !inFence {
			level := 0
			for level < len(trimmed) && level < 6 && trimmed[level] == '#' {
				level++
			}
			if level > 0 && (level == len(trimmed) || unicode.IsSpace(trimmed[level])) {
				name := strings.TrimSpace(string(trimmed[level:]))
				name = strings.TrimSpace(strings.TrimRight(name, "#"))
				headings = append(headings, heading{start: start, end: end, level: level, text: name})
			}
		}
		start = end
	}

	sections := make([]sectionRange, 0, len(headings)+1)
	if len(headings) == 0 {
		return []sectionRange{{Start: 0, End: len(text), HeadingStart: -1, HeadingEnd: -1}}
	}
	if headings[0].start > 0 {
		sections = append(sections, sectionRange{Start: 0, End: headings[0].start, HeadingStart: -1, HeadingEnd: -1})
	}
	stack := make([]string, 6)
	for i, current := range headings {
		for level := current.level - 1; level < len(stack); level++ {
			stack[level] = ""
		}
		stack[current.level-1] = current.text
		path := make([]string, 0, current.level)
		for level := 0; level < current.level; level++ {
			if stack[level] != "" {
				path = append(path, stack[level])
			}
		}
		end := len(text)
		if i+1 < len(headings) {
			end = headings[i+1].start
		}
		sections = append(sections, sectionRange{
			Start: current.start, End: end, HeadingStart: current.start, HeadingEnd: current.end,
			Level: current.level, Heading: current.text, Section: strings.Join(path, " > "),
		})
	}
	return sections
}

func fence(line []rune) (rune, int, bool) {
	if len(line) < 3 || (line[0] != '`' && line[0] != '~') {
		return 0, 0, false
	}
	marker := line[0]
	width := 0
	for width < len(line) && line[width] == marker {
		width++
	}
	return marker, width, width >= 3
}

func fixedChunks(documents []Document) []Chunk {
	var chunks []Chunk
	for _, document := range documents {
		sections := parseSections(document.Runes)
		ordinal := 0
		for start := 0; start < len(document.Runes); {
			end := fixedWindowEnd(document.Runes, start, fixedSize, fixedRewind)
			section, spans := sectionAtAndSpans(sections, start, end)
			chunks = append(chunks, newChunk("fixed", document, ordinal, section, spans, 0, start, end))
			ordinal++
			if end == len(document.Runes) {
				break
			}
			start = end - fixedOverlap
		}
	}
	return chunks
}

func fixedWindowEnd(text []rune, start, size, rewind int) int {
	wanted := min(len(text), start+size)
	if wanted == len(text) {
		return wanted
	}
	minimum := max(start, wanted-rewind)
	for i := wanted - 1; i >= minimum; i-- {
		if unicode.IsSpace(text[i]) {
			return i + 1
		}
	}
	return wanted
}

func sectionAtAndSpans(sections []sectionRange, start, end int) (string, int) {
	name := ""
	spans := 0
	for _, section := range sections {
		if section.Start <= start && start < section.End {
			name = section.Section
		}
		if start < section.End && end > section.Start {
			spans++
		}
	}
	if spans == 0 {
		spans = 1
	}
	return name, spans
}

func structureChunks(documents []Document) []Chunk {
	var chunks []Chunk
	for _, document := range documents {
		ordinal := 0
		for _, section := range parseSections(document.Runes) {
			contentStart := section.Start
			if section.HeadingStart >= 0 {
				contentStart = section.HeadingEnd
			}
			if strings.TrimSpace(string(document.Runes[contentStart:section.End])) == "" {
				continue
			}
			ranges := [][2]int{{section.Start, section.End}}
			if section.End-section.Start > structureMaxSize {
				ranges = splitStructureRange(document.Runes, section.Start, section.End)
			}
			for i, bounds := range ranges {
				part := 0
				if len(ranges) > 1 {
					part = i + 1
				}
				chunks = append(chunks, newChunk("structure", document, ordinal, section.Section, 1, part, bounds[0], bounds[1]))
				ordinal++
			}
		}
	}
	return chunks
}

func splitStructureRange(text []rune, start, end int) [][2]int {
	paragraphs := paragraphRanges(text, start, end)
	var result [][2]int
	currentStart, currentEnd := -1, -1
	flush := func() {
		if currentStart >= 0 {
			result = append(result, [2]int{currentStart, currentEnd})
			currentStart, currentEnd = -1, -1
		}
	}
	appendSmall := func(bounds [2]int) {
		if currentStart < 0 {
			currentStart, currentEnd = bounds[0], bounds[1]
			return
		}
		if bounds[1]-currentStart <= structureMaxSize {
			currentEnd = bounds[1]
			return
		}
		flush()
		currentStart, currentEnd = bounds[0], bounds[1]
	}
	for _, paragraph := range paragraphs {
		if paragraph[1]-paragraph[0] <= structureMaxSize {
			appendSmall(paragraph)
			continue
		}
		flush()
		for _, line := range lineRanges(text, paragraph[0], paragraph[1]) {
			if line[1]-line[0] <= structureMaxSize {
				appendSmall(line)
				continue
			}
			for cursor := line[0]; cursor < line[1]; {
				if currentStart < 0 {
					currentStart, currentEnd = cursor, cursor
				}
				available := structureMaxSize - (currentEnd - currentStart)
				if available == 0 {
					flush()
					continue
				}
				pieceEnd := fixedWindowEnd(text[:line[1]], cursor, available, min(fixedRewind, available))
				currentEnd = pieceEnd
				cursor = pieceEnd
				if cursor < line[1] {
					flush()
				}
			}
		}
	}
	flush()
	return result
}

func paragraphRanges(text []rune, start, end int) [][2]int {
	lines := lineRanges(text, start, end)
	var ranges [][2]int
	paragraphStart := start
	for _, line := range lines {
		if strings.TrimSpace(string(text[line[0]:line[1]])) == "" {
			if line[1] > paragraphStart {
				ranges = append(ranges, [2]int{paragraphStart, line[1]})
			}
			paragraphStart = line[1]
		}
	}
	if paragraphStart < end {
		ranges = append(ranges, [2]int{paragraphStart, end})
	}
	if len(ranges) == 0 && start < end {
		return [][2]int{{start, end}}
	}
	return ranges
}

func lineRanges(text []rune, start, end int) [][2]int {
	var ranges [][2]int
	for cursor := start; cursor < end; {
		lineEnd := cursor
		for lineEnd < end && text[lineEnd] != '\n' {
			lineEnd++
		}
		if lineEnd < end {
			lineEnd++
		}
		ranges = append(ranges, [2]int{cursor, lineEnd})
		cursor = lineEnd
	}
	return ranges
}

func newChunk(strategy string, document Document, ordinal int, section string, spans, part, start, end int) Chunk {
	return Chunk{
		ChunkID: fmt.Sprintf("%s:%s:%d", strategy, document.Source, ordinal), Strategy: strategy,
		Source: document.Source, Title: document.Title, Section: section, Spans: spans, Part: part,
		Start: start, End: end, Runes: end - start, Text: string(document.Runes[start:end]),
	}
}
