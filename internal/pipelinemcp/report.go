package pipelinemcp

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

const provenanceStart = "## Provenance\n\n```json\n"
const provenanceEnd = "\n```\n"

func ReportName(dataset Dataset, summarySHA string) string {
	return fmt.Sprintf("%s_%s_%s_%s.md", dataset.Currency, dataset.DateFrom, dataset.DateTo, summarySHA[:12])
}

// RenderReport is the single renderer used both by save_report and VerifyReport.
// Its provenance JSON is canonical and therefore byte-identical in the JS twin.
func RenderReport(dataset Dataset, datasetSHA string, summary Summary, summarySHA string) ([]byte, error) {
	provenance, err := CanonicalJSON(Provenance{Dataset: dataset, DatasetSHA256: datasetSHA, Summary: summary, SummarySHA256: summarySHA})
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	fmt.Fprintf(&out, "# Отчёт по %s: %s—%s\n\n", dataset.Currency, dataset.DateFrom, dataset.DateTo)
	fmt.Fprintf(&out, "За период курс %s изменился с %s до %s руб. за единицу: %s руб. (%s%%).\n\n", dataset.Currency, summary.First.UnitRate, summary.Last.UnitRate, summary.ChangeAbs, summary.ChangePct)
	out.WriteString("## Сводка\n\n| Показатель | Значение |\n|---|---:|\n")
	fmt.Fprintf(&out, "| Публикаций | %s |\n", summary.Count)
	fmt.Fprintf(&out, "| Первый курс (%s) | %s |\n", summary.First.Date, summary.First.UnitRate)
	fmt.Fprintf(&out, "| Последний курс (%s) | %s |\n", summary.Last.Date, summary.Last.UnitRate)
	fmt.Fprintf(&out, "| Минимум (%s) | %s |\n", summary.Min.Date, summary.Min.UnitRate)
	fmt.Fprintf(&out, "| Максимум (%s) | %s |\n", summary.Max.Date, summary.Max.UnitRate)
	fmt.Fprintf(&out, "| Среднее | %s |\n", summary.Mean)
	fmt.Fprintf(&out, "| Изменение, руб. | %s |\n", summary.ChangeAbs)
	fmt.Fprintf(&out, "| Изменение, %% | %s |\n\n", summary.ChangePct)
	out.WriteString("## Все публикации\n\n| Дата | Номинал | Курс, руб. | За единицу, руб. |\n|---|---:|---:|---:|\n")
	for _, row := range dataset.Rows {
		fmt.Fprintf(&out, "| %s | %s | %s | %s |\n", row.Date, row.Nominal, row.Value, row.UnitRate)
	}
	out.WriteByte('\n')
	out.WriteString(provenanceStart)
	out.WriteString(provenance)
	out.WriteString(provenanceEnd)
	return out.Bytes(), nil
}

func reportSHA(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// writeReportAtomic writes only a server-generated base name inside reportsDir.
func writeReportAtomic(reportsDir, name string, raw []byte) (string, error) {
	if filepath.Base(name) != name {
		return "", fmt.Errorf("некорректное имя отчёта")
	}
	if reportsDir == "" {
		return "", fmt.Errorf("папка отчётов не задана")
	}
	if err := os.MkdirAll(reportsDir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(reportsDir, name)
	if existing, err := os.ReadFile(path); err == nil && bytes.Equal(existing, raw) {
		return path, nil
	}
	temporary, err := os.CreateTemp(reportsDir, ".day19-report-*")
	if err != nil {
		return "", err
	}
	tmpName := temporary.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := temporary.Write(raw); err != nil {
		_ = temporary.Close()
		cleanup()
		return "", err
	}
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		cleanup()
		return "", err
	}
	if err := temporary.Close(); err != nil {
		cleanup()
		return "", err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return "", err
	}
	return path, nil
}
