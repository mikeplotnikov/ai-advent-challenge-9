package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func readReview(path, sha string, qs []QuestionRun) (map[string]Semantic, error) {
	out := map[string]Semantic{}
	raw, e := os.ReadFile(path)
	if os.IsNotExist(e) {
		return out, nil
	}
	if e != nil {
		return nil, e
	}
	var r Review
	if e = json.Unmarshal(raw, &r); e != nil {
		return nil, e
	}
	if r.RunSHA256 != sha {
		return nil, fmt.Errorf("semantic review run_sha256 mismatch")
	}
	if len(r.Questions) != len(qs) {
		return nil, fmt.Errorf("review needs every question")
	}
	ids := map[string]bool{}
	unknown := map[string]bool{}
	for _, q := range qs {
		ids[q.Question.ID] = true
		unknown[q.Question.ID] = q.Answer.Unknown
	}
	for _, s := range r.Questions {
		if !ids[s.ID] || out[s.ID].ID != "" || strings.TrimSpace(s.Reason) == "" {
			return nil, fmt.Errorf("invalid review id/reason")
		}
		switch s.Verdict {
		case "supported", "unsupported", "unknown":
		default:
			return nil, fmt.Errorf("invalid review verdict")
		}
		if (s.Verdict == "unknown" && !unknown[s.ID]) || (s.Verdict == "supported" && unknown[s.ID]) {
			return nil, fmt.Errorf("review verdict conflicts with refusal")
		}
		out[s.ID] = s
	}
	return out, nil
}
func buildReport(r Run, sha string, review map[string]Semantic) (Showcase, string) {
	data := Showcase{Meta: r.Meta, RunSHA256: sha, Prompts: promptTexts(), Questions: append([]QuestionRun(nil), r.Questions...), Results: map[string]int{"total": len(r.Questions), "substantive": 0, "unknown": 0, "source_pass": 0, "quote_pass": 0, "fact_correct": 0, "in_base_unknown": 0, "semantic_supported": 0, "semantic_unsupported": 0, "semantic_unknown": 0, "semantic_pending": 0}}
	var table strings.Builder
	table.WriteString("| ID | Тип | Ответ | Источники | Цитаты | Эталонные факты | Смысловая оценка |\n|---|---|---|---|---|---|---|\n")
	for i, q := range data.Questions {
		s, ok := review[q.Question.ID]
		if !ok {
			s = Semantic{q.Question.ID, "pending", "Независимая смысловая оценка ещё не выполнена."}
		}
		data.Questions[i].Semantic = s
		data.Results["semantic_"+s.Verdict]++
		state := "содержательный"
		if q.Answer.Unknown {
			state = "не знаю"
			data.Results["unknown"]++
			if q.Question.Kind == "in_base" {
				data.Results["in_base_unknown"]++
			}
		} else {
			data.Results["substantive"]++
			if q.Checks.Sources == "pass" {
				data.Results["source_pass"]++
			}
			if q.Checks.Quotes == "pass" {
				data.Results["quote_pass"]++
			}
		}
		if q.Checks.FactOutcome == "correct" {
			data.Results["fact_correct"]++
		}
		fmt.Fprintf(&table, "| %s | %s | %s | %s | %s | %s | %s |\n", q.Question.ID, q.Question.Kind, state, q.Checks.Sources, q.Checks.Quotes, q.Checks.FactOutcome, s.Verdict)
	}
	cost := "неизвестна"
	if r.Meta.TotalCostKnown {
		cost = fmt.Sprintf("$%.8f", r.Meta.TotalCostUSD)
	}
	text := fmt.Sprintf("# День 24 — сохранённый прогон\n\nRun SHA256: %s\n\nКод: %s. Модель: %s. R=1, вопросов: %d. Вызовов: %d. Стоимость: %s.\n\nИсточники: %d/%d содержательных ответов. Цитаты: %d/%d содержательных ответов. Отказы: %d/%d; потеря ответа in_base: %d/7.\n\n%s\nЭталонные факты — ограниченная проверка регулярными выражениями дня 22; она не оценивает весь смысл. Совпадение цитаты — дословный substring и проверка тройки source/section/chunk_id, а не доказательство поддержки каждого утверждения. Смысловая оценка — независимое экспертное суждение. Один прогон одной модели не доказывает отсутствие галлюцинаций. Уточнения чата ведущего за 01.10 не проверены.\n", sha, r.Meta.Commit, r.Meta.RequestedModel, len(r.Questions), r.Meta.ModelCalls, cost, data.Results["source_pass"], data.Results["substantive"], data.Results["quote_pass"], data.Results["substantive"], data.Results["unknown"], len(r.Questions), data.Results["in_base_unknown"], table.String())
	for _, q := range data.Questions {
		text += fmt.Sprintf("\n## %s\n\n%s\n\n%s\n\nУточнение: %s\n\nПричина отказа: %s\n\nСмысловая оценка: %s — %s\n", q.Question.ID, q.Question.Question, q.Answer.Answer, q.Answer.Clarification, q.Checks.RefusalReason, q.Semantic.Verdict, q.Semantic.Reason)
		for _, s := range q.Answer.Sources {
			text += fmt.Sprintf("\nИсточник: %s · %s · %s\n\n> %s\n", s.Source, s.Section, s.ChunkID, strings.ReplaceAll(s.Quote, "\n", "\n> "))
		}
	}
	return data, text
}

const readmeIntro = "# День 24 — цитаты и источники\n\nПродолжение дня 23: rewrite → bge-m3 → top-10 → cosine ≥ 0.45 → DeepSeek rerank ≥ 2 → top-3 → JSON-ответ. Требуются точные источники и дословные цитаты; отсутствие контекста вызывает локальный отказ без генерации. Ответ модели проверяется строго, один повтор при ошибке.\n\nКоманды из корня репозитория:\n\n\x60\x60\x60sh\ngo run ./day-24 -ask 'Что меняет temperature?'\ngo run ./day-24 -ask 'Что меняет temperature?' -cos 0.5 -min-score 2 -k-before 10 -k-after 3\ngo run ./day-24 -eval\ngo run ./day-24 -report\ngo test ./day-24 -count=1\ngo vet ./day-24\n\x60\x60\x60\n\nЖивые команды требуют Ollama bge-m3, существующий индекс дня 21 и ключ DEEPSEEK_API_KEY_DAY24 (либо DEEPSEEK_API_KEY) через штатный загрузчик настроек. Значения ключей не выводятся. -eval выполняет ровно 10 замороженных вопросов дня 22, не перезаписывает run.json. Повторный замер требует перенести исходный day-24/run.json, run.json.sha256 и semantic-review.json в архив вручную. -report работает без сети, проверяет хеши данных, промптов и кода; строит RESULTS.md, showcase.json и этот README.\n\nНезависимый проверяющий читает run.json, все ответы и цитаты. semantic-review.json имеет форму {run_sha256: SHA256 исходного run.json, questions: [{id, verdict, reason}]}; ровно 10 уникальных id, verdict supported/unsupported/unknown, непустая reason. Если файла нет, оценки pending.\n\n<!-- GENERATED RESULTS -->\n\n"

func writeReport(dir string) error {
	raw, e := os.ReadFile(filepath.Join(dir, "run.json"))
	if e != nil {
		return e
	}
	sha := digest(raw)
	seal, e := os.ReadFile(filepath.Join(dir, "run.json.sha256"))
	if e != nil {
		return e
	}
	if strings.TrimSpace(string(seal)) != sha {
		return fmt.Errorf("run.json SHA256 changed")
	}
	var r Run
	if e = json.Unmarshal(raw, &r); e != nil {
		return e
	}
	if e = validateRun(r); e != nil {
		return e
	}
	review, e := readReview(filepath.Join(dir, "semantic-review.json"), sha, r.Questions)
	if e != nil {
		return e
	}
	data, text := buildReport(r, sha, review)
	encoded, e := encodeJSON(data)
	if e != nil {
		return e
	}
	for _, f := range []struct {
		name string
		raw  []byte
	}{{"RESULTS.md", []byte(text)}, {"showcase.json", encoded}, {"README.md", []byte(readmeIntro + text)}} {
		if e = writeBytesAtomic(filepath.Join(dir, f.name), f.raw); e != nil {
			return e
		}
	}
	return nil
}
