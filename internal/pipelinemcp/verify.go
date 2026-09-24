package pipelinemcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

type VerifyCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
}

type VerifyResult struct {
	OK     bool          `json:"ok"`
	Checks []VerifyCheck `json:"checks"`
}

func formatFailure(reason string) VerifyResult {
	return VerifyResult{Checks: []VerifyCheck{{Name: "формат", OK: false, Reason: reason}}}
}

// VerifyReport never trusts the human-readable part. It parses the canonical
// provenance block, recomputes both hashes and the summary from dataset rows,
// then renders the entire report again and compares every byte.
func VerifyReport(raw []byte) VerifyResult {
	if len(raw) == 0 {
		return formatFailure("пустой файл")
	}
	if len(raw) > MaxReportBytes {
		return formatFailure("файл больше 64 КиБ")
	}
	if !utf8.Valid(raw) {
		return formatFailure("файл не является UTF-8")
	}
	start := bytes.Index(raw, []byte(provenanceStart))
	if start < 0 {
		return formatFailure("нет блока provenance JSON")
	}
	jsonStart := start + len(provenanceStart)
	endOffset := bytes.Index(raw[jsonStart:], []byte(provenanceEnd))
	if endOffset < 0 {
		return formatFailure("блок provenance JSON не закрыт")
	}
	jsonEnd := jsonStart + endOffset
	if jsonEnd+len(provenanceEnd) != len(raw) {
		return formatFailure("после блока provenance есть лишние данные")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw[jsonStart:jsonEnd]))
	decoder.DisallowUnknownFields()
	var provenance Provenance
	if err := decoder.Decode(&provenance); err != nil {
		return formatFailure("битый provenance JSON: " + err.Error())
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return formatFailure("в provenance JSON больше одного значения")
		}
		return formatFailure("битый provenance JSON: " + err.Error())
	}
	if err := validateDataset(provenance.Dataset); err != nil {
		return formatFailure(err.Error())
	}

	_, datasetSHA, err := HashCanonical(provenance.Dataset)
	if err != nil {
		return formatFailure(err.Error())
	}
	recomputed, err := Summarize(provenance.Dataset, datasetSHA)
	if err != nil {
		return formatFailure("сводку нельзя пересчитать: " + err.Error())
	}
	_, summarySHA, err := HashCanonical(recomputed)
	if err != nil {
		return formatFailure(err.Error())
	}
	checks := []VerifyCheck{
		{Name: "dataset_sha256", OK: provenance.DatasetSHA256 == datasetSHA, Reason: mismatch(provenance.DatasetSHA256, datasetSHA)},
		{Name: "сводка", OK: equalCanonical(provenance.Summary, recomputed), Reason: "сводка не совпадает с пересчётом строк"},
		{Name: "summary_sha256", OK: provenance.SummarySHA256 == summarySHA, Reason: mismatch(provenance.SummarySHA256, summarySHA)},
		{Name: "цепочка", OK: provenance.Summary.DatasetSHA == datasetSHA, Reason: mismatch(provenance.Summary.DatasetSHA, datasetSHA)},
	}
	expected, renderErr := RenderReport(provenance.Dataset, datasetSHA, recomputed, summarySHA)
	if renderErr != nil {
		return formatFailure("отчёт нельзя отрендерить: " + renderErr.Error())
	}
	checks = append(checks, VerifyCheck{Name: "файл", OK: bytes.Equal(raw, expected), Reason: "байты файла не совпадают с повторным рендером"})
	result := VerifyResult{OK: true, Checks: checks}
	for i := range result.Checks {
		if result.Checks[i].OK {
			result.Checks[i].Reason = ""
		} else {
			result.OK = false
		}
	}
	return result
}

func validateDataset(dataset Dataset) error {
	if strings.TrimSpace(dataset.Currency) == "" || strings.TrimSpace(dataset.CBRID) == "" || strings.TrimSpace(dataset.Name) == "" || dataset.DateFrom == "" || dataset.DateTo == "" || dataset.Source == "" {
		return fmt.Errorf("в датасете отсутствуют обязательные поля")
	}
	if len(dataset.Rows) == 0 {
		return fmt.Errorf("датасет не содержит публикаций")
	}
	previous := ""
	for i, row := range dataset.Rows {
		if row.Date == "" || row.Nominal == "" || row.Value == "" || row.UnitRate == "" {
			return fmt.Errorf("строка %d датасета неполна", i+1)
		}
		if previous != "" && row.Date < previous {
			return fmt.Errorf("строки датасета не отсортированы по дате")
		}
		previous = row.Date
		if nominal, err := strconv.ParseInt(row.Nominal, 10, 64); err != nil || nominal <= 0 || strconv.FormatInt(nominal, 10) != row.Nominal {
			return fmt.Errorf("строка %d nominal: ожидалось положительное целое", i+1)
		}
		value, err := parseMicros(row.Value)
		if err != nil {
			return fmt.Errorf("строка %d value: %v", i+1, err)
		}
		if value < 0 {
			return fmt.Errorf("строка %d value: курс не может быть отрицательным", i+1)
		}
		unitRate, err := parseMicros(row.UnitRate)
		if err != nil {
			return fmt.Errorf("строка %d unit_rate: %v", i+1, err)
		}
		if unitRate < 0 {
			return fmt.Errorf("строка %d unit_rate: курс не может быть отрицательным", i+1)
		}
	}
	return nil
}

func equalCanonical(left, right any) bool {
	a, errA := CanonicalJSON(left)
	b, errB := CanonicalJSON(right)
	return errA == nil && errB == nil && a == b
}

func mismatch(got, want string) string {
	return fmt.Sprintf("получено %s, пересчитано %s", got, want)
}
