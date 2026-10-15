package main

import (
	"fmt"
	"strings"
	"time"
)

// marker identifies the review comment, so a new push edits it instead of adding another.
const marker = "<!-- day-32-ai-review -->"

type metrics struct {
	Started      time.Time
	Collect      time.Duration
	Index        time.Duration
	Retrieve     time.Duration
	Model        time.Duration
	Corpus       corpus
	Embed        embedStats
	Degraded     string // why retrieval fell back to lexical search; empty when it did not
	Calls        callStats
	ReviewFailed string
}

func renderReview(pr pullRequest, v verdict, sources []source, m metrics) string {
	var b strings.Builder
	b.WriteString(marker + "\n")
	fmt.Fprintf(&b, "## 🤖 AI-ревью · день 32\n\nКоммит `%s` относительно `%s`, файлов: %d.\n\n", short(pr.Head), short(pr.Base), len(pr.Changes))
	if m.Degraded != "" {
		fmt.Fprintf(&b, "> ⚠️ **Деградация:** эмбеддинги недоступны (%s), фрагменты подобраны поиском по словам — контекст может быть менее точным.\n\n", inline(m.Degraded))
	}
	b.WriteString(inline(v.Summary) + "\n")
	section(&b, "🐞 Потенциальные баги", v.Bugs)
	section(&b, "🏛 Архитектурные проблемы", v.Architecture)
	section(&b, "💡 Рекомендации", v.Recommendations)
	if len(m.Calls.Dropped) > 0 {
		b.WriteString("\n> Отброшено при проверке ответа модели (после повторного запроса):\n")
		for _, d := range m.Calls.Dropped {
			b.WriteString("> - " + inline(d) + "\n")
		}
	}
	notes(&b, pr)
	b.WriteString("\n<details><summary>Контекст из RAG</summary>\n\n")
	for _, s := range sources {
		fmt.Fprintf(&b, "- **%s** %s (оценка %.3f)\n", s.ID, inline(s.location()), s.Score)
	}
	b.WriteString("\n</details>\n")
	footer(&b, m)
	return b.String()
}

func renderFailure(pr pullRequest, reason string, m metrics) string {
	var b strings.Builder
	b.WriteString(marker + "\n")
	fmt.Fprintf(&b, "## 🤖 AI-ревью · день 32\n\n❌ **Ревью не выполнено** для `%s`: %s\n\nPR нужно посмотреть вручную; перезапуск — повторный запуск job или новый пуш.\n", short(pr.Head), inline(truncateRunes(reason, 800)))
	notes(&b, pr)
	footer(&b, m)
	return b.String()
}

func section(b *strings.Builder, title string, items []finding) {
	fmt.Fprintf(b, "\n### %s\n\n", title)
	if len(items) == 0 {
		b.WriteString("Не найдено.\n")
		return
	}
	for _, f := range items {
		where := "`" + f.File + "`"
		if f.Line > 0 {
			where = fmt.Sprintf("`%s:%d`", f.File, f.Line)
		}
		fmt.Fprintf(b, "- %s — **%s** %s _(источники: %s)_\n", where, inline(f.Title), inline(f.Detail), inline(strings.Join(f.Sources, ", ")))
	}
}

func notes(b *strings.Builder, pr pullRequest) {
	if len(pr.Omitted) == 0 {
		return
	}
	b.WriteString("\n> Не передано модели целиком:\n")
	for _, o := range pr.Omitted {
		b.WriteString("> - " + inline(o) + "\n")
	}
}

func footer(b *strings.Builder, m metrics) {
	b.WriteString("\n---\n")
	mode := "эмбеддинги bge-m3"
	if m.Degraded != "" {
		mode = "поиск по словам (деградация)"
	}
	fmt.Fprintf(b, "<sub>Модель `%s` · поиск: %s · база: документов %d, Go-файлов %d, фрагментов %d (из кэша %d, посчитано %d)<br>",
		m.Calls.Model, mode, m.Corpus.DocFiles, m.Corpus.CodeFiles, len(m.Corpus.Pieces), m.Embed.FromCache, m.Embed.Embedded)
	cost := "цена не посчитана"
	if m.Calls.Priced {
		cost = fmt.Sprintf("$%.5f", m.Calls.CostUSD)
	}
	fmt.Fprintf(b, "Время: diff %s · индекс %s · поиск %s · модель %s · всего %s · вызовов %d (повторов %d%s) · токенов: вход %d (из кэша %d), выход %d · %s<br>",
		secs(m.Collect), secs(m.Index), secs(m.Retrieve), secs(m.Model), secs(time.Since(m.Started)),
		m.Calls.Calls, m.Calls.Retries, repaired(m.Calls.Repaired),
		m.Calls.Usage.PromptTokens, m.Calls.Usage.PromptCacheHitTokens, m.Calls.Usage.CompletionTokens, cost)
	b.WriteString("Ревью не блокирует мерж — решение за человеком. Оцените пользу реакцией 👍 / 👎 на этот комментарий.</sub>\n")
}

func repaired(r bool) string {
	if r {
		return ", исправление формата"
	}
	return ""
}

func secs(d time.Duration) string { return fmt.Sprintf("%.1f с", d.Seconds()) }

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// inline makes model or file text safe for one line of the comment: no line breaks,
// no HTML comment that could forge the marker, and no @mention that would ping someone.
func inline(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	text = strings.ReplaceAll(text, "<!--", "&lt;!--")
	text = strings.ReplaceAll(text, "@", "@​")
	return text
}
