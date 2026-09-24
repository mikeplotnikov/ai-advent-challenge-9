// Package pipelinemcp implements the day 19 CBR tool-composition server.
package pipelinemcp

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"

	"github.com/mikeplotnikov/ai-advent-challenge-9/internal/cbr"
)

const (
	ServerName     = "cbr-pipeline"
	ServerVersion  = "1.0.0"
	MaxRangeDays   = 93
	MaxReportBytes = 64 << 10
	Source         = "cbr.ru XML_dynamic"
)

// DatasetRow has no JSON numbers. The JS twin must hash exactly these strings:
// nominal is a base-10 integer; value and unit_rate have exactly six fractional
// digits. All object keys are sorted by CanonicalJSON, regardless of struct order.
type DatasetRow struct {
	Date     string `json:"date"`
	Nominal  string `json:"nominal"`
	UnitRate string `json:"unit_rate"`
	Value    string `json:"value"`
}

type Dataset struct {
	CBRID    string       `json:"cbr_id"`
	Currency string       `json:"currency"`
	DateFrom string       `json:"date_from"`
	DateTo   string       `json:"date_to"`
	Name     string       `json:"name"`
	Rows     []DatasetRow `json:"rows"`
	Source   string       `json:"source"`
}

type SummaryPoint struct {
	Date     string `json:"date"`
	UnitRate string `json:"unit_rate"`
}

// Summary likewise carries every number as a string. count is a base-10
// integer; every monetary/percentage field has exactly six fractional digits.
type Summary struct {
	ChangeAbs  string       `json:"change_abs"`
	ChangePct  string       `json:"change_pct"`
	Count      string       `json:"count"`
	DatasetSHA string       `json:"dataset_sha256"`
	First      SummaryPoint `json:"first"`
	Last       SummaryPoint `json:"last"`
	Max        SummaryPoint `json:"max"`
	Mean       string       `json:"mean"`
	Min        SummaryPoint `json:"min"`
}

type Provenance struct {
	Dataset       Dataset `json:"dataset"`
	DatasetSHA256 string  `json:"dataset_sha256"`
	Summary       Summary `json:"summary"`
	SummarySHA256 string  `json:"summary_sha256"`
}

func DatasetFromRange(value cbr.RangeRates) Dataset {
	rows := make([]DatasetRow, 0, len(value.Rows))
	for _, row := range value.Rows {
		rows = append(rows, DatasetRow{Date: row.Date, Nominal: strconv.FormatInt(row.Nominal, 10), Value: formatMicros(row.ValueMicros), UnitRate: formatMicros(row.UnitRateMicros)})
	}
	return Dataset{Currency: value.Currency.Code, CBRID: value.Currency.ID, Name: value.Currency.Name, DateFrom: value.DateFrom, DateTo: value.DateTo, Source: value.Source, Rows: rows}
}

func HashCanonical(value any) (string, string, error) {
	canonical, err := CanonicalJSON(value)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256([]byte(canonical))
	return canonical, hex.EncodeToString(sum[:]), nil
}

func shortID(prefix, hash string) string {
	return prefix + hash[:12]
}

func formatMicros(value int64) string {
	sign := ""
	if value < 0 {
		sign = "-"
		value = -value
	}
	return fmt.Sprintf("%s%d.%06d", sign, value/1_000_000, value%1_000_000)
}

func parseMicros(value string) (int64, error) {
	if value == "" {
		return 0, fmt.Errorf("пустое число")
	}
	sign := int64(1)
	if strings.HasPrefix(value, "-") {
		sign, value = -1, strings.TrimPrefix(value, "-")
	}
	parts := strings.Split(value, ".")
	if len(parts) != 2 || len(parts[1]) != 6 || parts[0] == "" {
		return 0, fmt.Errorf("ожидалось число с шестью знаками после точки: %q", value)
	}
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || whole < 0 {
		return 0, fmt.Errorf("некорректное число %q", value)
	}
	fraction, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("некорректное число %q", value)
	}
	if whole > (math.MaxInt64-fraction)/1_000_000 {
		return 0, fmt.Errorf("число вне допустимого диапазона %q", value)
	}
	return sign * (whole*1_000_000 + fraction), nil
}

func roundBigDivAway(numerator *big.Int, denominator int64) (*big.Int, error) {
	if denominator <= 0 {
		return nil, fmt.Errorf("делитель должен быть положительным")
	}
	den := big.NewInt(denominator)
	abs := new(big.Int).Abs(new(big.Int).Set(numerator))
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(abs, den, remainder)
	if new(big.Int).Lsh(remainder, 1).Cmp(den) >= 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if numerator.Sign() < 0 {
		quotient.Neg(quotient)
	}
	return quotient, nil
}

func Summarize(dataset Dataset, datasetSHA string) (Summary, error) {
	if len(dataset.Rows) == 0 {
		return Summary{}, fmt.Errorf("датасет не содержит публикаций")
	}
	values := make([]int64, len(dataset.Rows))
	for i, row := range dataset.Rows {
		value, err := parseMicros(row.UnitRate)
		if err != nil {
			return Summary{}, fmt.Errorf("строка %d: %w", i+1, err)
		}
		values[i] = value
	}
	minIndex, maxIndex := 0, 0
	sum := new(big.Int)
	for i, value := range values {
		sum.Add(sum, big.NewInt(value))
		if value < values[minIndex] {
			minIndex = i
		}
		if value > values[maxIndex] {
			maxIndex = i
		}
	}
	point := func(index int) SummaryPoint {
		return SummaryPoint{Date: dataset.Rows[index].Date, UnitRate: dataset.Rows[index].UnitRate}
	}
	change := values[len(values)-1] - values[0]
	if values[0] == 0 {
		return Summary{}, fmt.Errorf("первый курс равен нулю")
	}
	mean, err := roundBigDivAway(sum, int64(len(values)))
	if err != nil || !mean.IsInt64() {
		return Summary{}, fmt.Errorf("среднее вне допустимого диапазона")
	}
	pctNumerator := new(big.Int).Mul(big.NewInt(change), big.NewInt(100*1_000_000))
	changePct, err := roundBigDivAway(pctNumerator, values[0])
	if err != nil || !changePct.IsInt64() {
		return Summary{}, fmt.Errorf("изменение в процентах вне допустимого диапазона")
	}
	return Summary{
		DatasetSHA: datasetSHA,
		Count:      strconv.Itoa(len(dataset.Rows)),
		First:      point(0),
		Last:       point(len(dataset.Rows) - 1),
		Min:        point(minIndex),
		Max:        point(maxIndex),
		Mean:       formatMicros(mean.Int64()),
		ChangeAbs:  formatMicros(change),
		ChangePct:  formatMicros(changePct.Int64()),
	}, nil
}
