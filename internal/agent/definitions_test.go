package agent

import (
	"strings"
	"testing"
)

func TestDefinitionsContainARealCompressedRequest(t *testing.T) {
	defs, err := BuildDefinitions("роль")
	if err != nil {
		t.Fatalf("BuildDefinitions: %v", err)
	}
	c := defs.Compression
	if c.KeepLastMessages != 2 || c.Summary != "summary-1" || len(c.Turns) != 1 || c.Turns[0] != "второй" || c.Next != "третий" {
		t.Fatalf("сжатый пример не описывает ожидаемый сырой хвост: %+v", c)
	}
	if got := len(c.Sent); got != 4 || c.Sent[0].Role != "system" || c.Sent[1].Content != "второй" || c.Sent[2].Content != "ответ 2" || c.Sent[3].Content != "третий" {
		t.Fatalf("сжатый пример не записал форму запроса: %+v", c.Sent)
	}
	if !strings.Contains(c.Sent[0].Content, summaryHeader+c.Summary) {
		t.Fatalf("сжатый пример не поместил summary в system-контекст: %q", c.Sent[0].Content)
	}
	if len(c.SummarySent) != 2 || c.SummarySent[0].Role != "system" || c.SummarySent[1].Role != "user" ||
		c.SummaryMaxTokens != summaryMaxTokens || c.SummaryThinking != "disabled" {
		t.Fatalf("сжатый пример не записал полный запрос к summary: %+v", c)
	}
}
