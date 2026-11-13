package eval

import (
	"bytes"
	"encoding/csv"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

func summaryCSV(items []TaskSummary) [][]string {
	rows := [][]string{{"task", "participants", "success_rate", "median_seconds", "p90_seconds", "top_errors"}}
	for _, item := range items {
		codes := make([]string, 0, len(item.TopErrors))
		for code := range item.TopErrors {
			codes = append(codes, code)
		}
		sort.Strings(codes)
		errors := make([]string, 0, len(codes))
		for _, code := range codes {
			// Escape the separator characters and the escape character itself.
			// Otherwise legal codes such as "a:1;b" collide with two entries.
			escaped := strings.NewReplacer(`\`, `\\`, ":", `\:`, ";", `\;`).Replace(code)
			errors = append(errors, escaped+":"+strconv.Itoa(item.TopErrors[code]))
		}
		rows = append(rows, []string{
			item.Task, strconv.Itoa(item.Participants), optionalFloat(item.SuccessRate),
			optionalFloat(item.MedianSeconds), optionalFloat(item.P90Seconds), strings.Join(errors, ";"),
		})
	}
	return rows
}

func eventsCSV(items []Event) [][]string {
	rows := [][]string{{"id", "participant_code", "task", "event", "success", "error_code", "client_ts", "received_at"}}
	for _, item := range items {
		success := ""
		if item.Success != nil {
			success = strconv.FormatBool(*item.Success)
		}
		rows = append(rows, []string{
			strconv.FormatInt(item.ID, 10), item.ParticipantCode, item.Task, item.Event,
			success, optionalString(item.ErrorCode), optionalString(item.ClientTS),
			item.ReceivedAt.Format(time.RFC3339Nano),
		})
	}
	return rows
}

func encodeCSV(rows [][]string) ([]byte, error) {
	var output bytes.Buffer
	writer := csv.NewWriter(&output)
	for _, row := range rows {
		safe := make([]string, len(row))
		for i, value := range row {
			safe[i] = safeCSVCell(value)
		}
		if err := writer.Write(safe); err != nil {
			return nil, err
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func safeCSVCell(value string) string {
	if value == "" {
		return value
	}
	trimmed := strings.TrimLeftFunc(value, func(r rune) bool { return unicode.IsSpace(r) || r == '\ufeff' })
	// Escape an existing leading apostrophe as well, so consumers can remove
	// exactly one leading apostrophe without confusing literal and safe text.
	if strings.ContainsAny(value[:1], "'\t\r\n") || len(trimmed) > 0 && strings.ContainsAny(trimmed[:1], "=+-@") {
		return "'" + value
	}
	return value
}

func optionalFloat(value *float64) string {
	if value == nil {
		return ""
	}
	return strconv.FormatFloat(*value, 'f', -1, 64)
}

func optionalString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
