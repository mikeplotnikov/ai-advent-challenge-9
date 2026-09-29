package main

import (
	"strings"
	"testing"
)

func testDocument(source, text string) Document {
	runes := []rune(text)
	return Document{Source: source, Title: documentTitle(runes, source), Text: text, Runes: runes}
}

func TestFixedChunksUseRunesAndExactOverlap(t *testing.T) {
	text := strings.Repeat("я", 4999) + " "
	firstDoc := testDocument("one.md", text)
	secondDoc := testDocument("two.md", strings.Repeat("ю", 2000))
	chunks := fixedChunks([]Document{firstDoc, secondDoc})
	var first []Chunk
	for _, chunk := range chunks {
		if chunk.Source == "one.md" {
			first = append(first, chunk)
		}
		if chunk.Start < 0 || chunk.End > len([]rune(map[string]string{"one.md": text, "two.md": strings.Repeat("ю", 2000)}[chunk.Source])) {
			t.Fatalf("чанк вышел за границу файла: %+v", chunk)
		}
	}
	for i, chunk := range first {
		if i < len(first)-1 && (chunk.Runes < 1700 || chunk.Runes > 1800) {
			t.Errorf("чанк %d: %d рун", i, chunk.Runes)
		}
		if i > 0 && first[i-1].End-chunk.Start != fixedOverlap {
			t.Errorf("перекрытие %d и %d = %d", i-1, i, first[i-1].End-chunk.Start)
		}
	}
	covered := make([]bool, len(firstDoc.Runes))
	for _, chunk := range first {
		for i := chunk.Start; i < chunk.End; i++ {
			covered[i] = true
		}
	}
	for i, ok := range covered {
		if !ok {
			t.Fatalf("руна %d потеряна", i)
		}
	}
}

func TestFixedWindowRewindsToTheNearestWhitespace(t *testing.T) {
	text := []rune(strings.Repeat("я", 1710) + " " + strings.Repeat("ю", 50) + " " + strings.Repeat("э", 300))
	end := fixedWindowEnd(text, 0, fixedSize, fixedRewind)
	if end != 1762 {
		t.Fatalf("end=%d, ожидалась позиция сразу после ближайшего пробела 1762", end)
	}
	withoutWhitespace := []rune(strings.Repeat("я", 2000))
	if end := fixedWindowEnd(withoutWhitespace, 0, fixedSize, fixedRewind); end != fixedSize {
		t.Fatalf("без пробела end=%d, ожидалось %d", end, fixedSize)
	}
}

func TestStructureHeadingsFencePreambleAndEmptyParent(t *testing.T) {
	text := "вступление\n\n# A\n```text\n# не заголовок\n```\n## B\n## Пустой\n### Ребёнок\nтекст\n## C\nещё\n"
	document := testDocument("doc.md", text)
	sections := parseSections(document.Runes)
	want := []string{"", "A", "A > B", "A > Пустой", "A > Пустой > Ребёнок", "A > C"}
	if len(sections) != len(want) {
		t.Fatalf("разделов %d, ожидалось %d: %+v", len(sections), len(want), sections)
	}
	for i := range want {
		if sections[i].Section != want[i] {
			t.Errorf("раздел %d = %q, ожидался %q", i, sections[i].Section, want[i])
		}
	}
	chunks := structureChunks([]Document{document})
	for _, chunk := range chunks {
		if chunk.Section == "A > B" || chunk.Section == "A > Пустой" {
			t.Errorf("раздел только из заголовка стал чанком: %+v", chunk)
		}
	}
	if chunks[0].Section != "" || !strings.HasPrefix(chunks[0].Text, "вступление") {
		t.Fatalf("пreamble потерян: %+v", chunks[0])
	}
	fixed := fixedChunks([]Document{document})
	if fixed[0].Section != "" {
		t.Fatalf("fixed-чанк с start=0 получил section=%q", fixed[0].Section)
	}
}

func TestFixedSectionAndSpanMetadata(t *testing.T) {
	text := "# A\n" + strings.Repeat("а", 994) + "\n## B\n" + strings.Repeat("б", 992) + "\n## C\n" + strings.Repeat("в", 992)
	document := testDocument("three.md", text)
	chunks := fixedChunks([]Document{document})
	if len(chunks) < 2 {
		t.Fatalf("чанков %d", len(chunks))
	}
	if chunks[0].Section != "A" || chunks[0].Spans != 2 {
		t.Fatalf("первый чанк: section=%q spans=%d", chunks[0].Section, chunks[0].Spans)
	}
	last := chunks[len(chunks)-1]
	if last.Section != "A > B" || last.Spans != 2 {
		t.Fatalf("последний чанк: section=%q spans=%d", last.Section, last.Spans)
	}
	short := testDocument("short.md", "# A\nкороткий раздел")
	if got := fixedChunks([]Document{short})[0].Spans; got != 1 {
		t.Fatalf("чанк внутри одного раздела: spans=%d", got)
	}
}

func TestStructureSplitsParagraphsLinesAndLongLines(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"абзацы", strings.Repeat("а", 2000) + "\n\n" + strings.Repeat("б", 2000)},
		{"строки", strings.Repeat("а", 2000) + "\n" + strings.Repeat("б", 2000) + "\n"},
		{"длинная строка", strings.Repeat("я", 7300)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			document := testDocument(tc.name+".md", "# Раздел\n"+tc.body)
			chunks := structureChunks([]Document{document})
			if len(chunks) < 2 {
				t.Fatalf("раздел не поделён: %d чанков", len(chunks))
			}
			cursor := 0
			for i, chunk := range chunks {
				if chunk.Runes > structureMaxSize || chunk.Section != "Раздел" || chunk.Part != i+1 {
					t.Errorf("чанк %d: runes=%d section=%q part=%d", i, chunk.Runes, chunk.Section, chunk.Part)
				}
				if i == 0 {
					cursor = chunk.Start
				}
				if chunk.Start != cursor {
					t.Fatalf("между частями потеряно: cursor=%d start=%d", cursor, chunk.Start)
				}
				cursor = chunk.End
			}
			if cursor != len(document.Runes) {
				t.Fatalf("конец %d, длина %d", cursor, len(document.Runes))
			}
			switch tc.name {
			case "абзацы":
				if !strings.HasSuffix(chunks[0].Text, "\n\n") || !strings.HasPrefix(chunks[1].Text, "б") {
					t.Fatalf("граница прошла не по пустой строке: %q | %q", chunks[0].Text[len(chunks[0].Text)-4:], chunks[1].Text[:4])
				}
			case "строки":
				if !strings.HasSuffix(chunks[0].Text, "\n") || !strings.HasPrefix(chunks[1].Text, "б") {
					t.Fatal("длинный абзац поделён не по строке")
				}
			case "длинная строка":
				if chunks[0].Runes != structureMaxSize || !strings.HasPrefix(chunks[0].Text, "# Раздел\n") || strings.TrimSpace(chunks[0].Text) == "# Раздел" {
					t.Fatalf("первое окно длинной строки: runes=%d text=%q", chunks[0].Runes, chunks[0].Text[:min(20, len(chunks[0].Text))])
				}
			}
		})
	}
}
