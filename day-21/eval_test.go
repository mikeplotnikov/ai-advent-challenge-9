package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func questionFixture(documentCount int) ([]Question, []Document) {
	texts := make([]strings.Builder, documentCount)
	for i := range texts {
		fmt.Fprintf(&texts[i], "# Документ %d\n", i)
	}
	questions := make([]Question, 40)
	for i := range questions {
		sourceIndex := i % documentCount
		evidence := fmt.Sprintf("Уникальная цитата %02d сообщает проверяемый факт о работе системы без повторения в других документах.", i)
		fmt.Fprintln(&texts[sourceIndex], evidence)
		fmt.Fprintln(&texts[sourceIndex])
		questions[i] = Question{ID: fmt.Sprintf("q%02d", i), Question: fmt.Sprintf("Какой факт относится к случаю %02d?", i), Source: fmt.Sprintf("doc-%02d.md", sourceIndex), Evidence: evidence}
	}
	documents := make([]Document, documentCount)
	for i := range documents {
		documents[i] = testDocument(fmt.Sprintf("doc-%02d.md", i), texts[i].String())
	}
	return questions, documents
}

func TestQuestionValidatorEnforcesFrozenSetRules(t *testing.T) {
	questions, documents := questionFixture(15)
	if err := validateQuestions(questions, documents); err != nil {
		t.Fatalf("валидная фикстура: %v", err)
	}
	tests := []struct {
		name string
		edit func([]Question, []Document) ([]Question, []Document)
		want string
	}{
		{"unique id", func(q []Question, d []Document) ([]Question, []Document) { q[1].ID = q[0].ID; return q, d }, "повторный id"},
		{"exact count", func(q []Question, d []Document) ([]Question, []Document) { return q[:39], d }, "нужно 40"},
		{"evidence length", func(q []Question, d []Document) ([]Question, []Document) {
			q[0].Evidence = "коротко"
			return q, d
		}, "длиной"},
		{"unique evidence", func(q []Question, d []Document) ([]Question, []Document) {
			d[1] = testDocument(d[1].Source, d[1].Text+"\n"+q[0].Evidence)
			return q, d
		}, "встречается в корпусе 2"},
		{"no four word leak", func(q []Question, d []Document) ([]Question, []Document) {
			q[0].Question = strings.Join(wordTokens(q[0].Evidence)[:4], " ")
			return q, d
		}, "четыре слова"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			q, d := questionFixture(15)
			q, d = tc.edit(q, d)
			if err := validateQuestions(q, d); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ошибка=%v, ожидалась строка %q", err, tc.want)
			}
		})
	}
	limitedQuestions, limitedDocuments := questionFixture(14)
	if err := validateQuestions(limitedQuestions, limitedDocuments); err == nil || !strings.Contains(err.Error(), "нужно не меньше 15") {
		t.Fatalf("покрытие файлов: %v", err)
	}
	structureQuestions, structureDocuments := questionFixture(15)
	prefix := strings.Repeat("а", 30)
	suffix := strings.Repeat("б", 30)
	structureQuestions[0].Evidence = prefix + "\n## Второй\n" + suffix
	structureDocuments[0] = testDocument(structureDocuments[0].Source, "# Первый\n"+structureQuestions[0].Evidence+"\n\n"+structureDocuments[0].Text)
	if err := validateQuestions(structureQuestions, structureDocuments); err == nil || !strings.Contains(err.Error(), "structure-чанк") {
		t.Fatalf("вместимость structure не проверена валидатором: %v", err)
	}
}

func TestEvidenceMustFitBothStrategies(t *testing.T) {
	document := testDocument("split.md", "# A\n"+strings.Repeat("а", 60)+"\n## B\n"+strings.Repeat("б", 60))
	evidence := string(document.Runes[45:105])
	question := Question{Source: document.Source, Evidence: evidence}
	if !evidenceFits(question, fixedChunks([]Document{document})) {
		t.Fatal("цитата должна помещаться в короткий fixed-чанк")
	}
	if evidenceFits(question, structureChunks([]Document{document})) {
		t.Fatal("цитата через границу заголовка неожиданно поместилась в structure-чанк")
	}
}

func TestFrozenQuestions(t *testing.T) {
	path := "eval/questions.json"
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Skip("вопросов ещё нет")
	} else if err != nil {
		t.Fatal(err)
	}
	questions, _, err := loadQuestions(path)
	if err != nil {
		t.Fatal(err)
	}
	documents, _, _, err := loadCorpus("corpus")
	if err != nil {
		t.Fatal(err)
	}
	if err := validateQuestions(questions, documents); err != nil {
		t.Fatal(err)
	}
}

func TestResultsCarryCurrentQuestionsHashAndCommitOrder(t *testing.T) {
	if _, err := os.Stat("RESULTS.md"); os.IsNotExist(err) {
		t.Skip("RESULTS.md ещё не снят")
	} else if err != nil {
		t.Fatal(err)
	}
	digest, err := sha256File("eval/questions.json")
	if err != nil {
		t.Fatal(err)
	}
	results, err := os.ReadFile("RESULTS.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(results), "Вопросы: sha256 "+digest) {
		t.Fatalf("RESULTS.md не содержит sha256 текущих вопросов %s", digest)
	}
	firstCommit := func(path string) string {
		t.Helper()
		output, err := exec.Command("git", "log", "--follow", "--format=%H", "--", path).Output()
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Fields(string(output))
		if len(lines) == 0 {
			t.Fatalf("%s ещё не закоммичен", path)
		}
		return lines[len(lines)-1]
	}
	questionsCommit := firstCommit(":(top)day-21/eval/questions.json")
	resultsCommit := firstCommit(":(top)day-21/RESULTS.md")
	if questionsCommit == resultsCommit {
		t.Fatal("questions.json и первый RESULTS.md попали в один коммит")
	}
	if err := exec.Command("git", "merge-base", "--is-ancestor", questionsCommit, resultsCommit).Run(); err != nil {
		t.Fatalf("коммит вопросов %s не предшествует первому RESULTS %s", questionsCommit, resultsCommit)
	}
}

func TestReadmeCarriesGeneratedResults(t *testing.T) {
	results, err := os.ReadFile("RESULTS.md")
	if os.IsNotExist(err) {
		t.Skip("RESULTS.md ещё не снят")
	}
	if err != nil {
		t.Fatal(err)
	}
	readme, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatal(err)
	}
	rows := 0
	for _, line := range strings.Split(string(results), "\n") {
		if strings.HasPrefix(line, "|") || strings.HasPrefix(line, "Коммит:") || strings.HasPrefix(line, "Вопросы:") {
			rows++
			if !strings.Contains(string(readme), line) {
				t.Errorf("в README нет строки отчёта:\n%s", line)
			}
		}
	}
	if rows < 50 {
		t.Fatalf("в RESULTS.md найдено только %d переносимых строк", rows)
	}
}
