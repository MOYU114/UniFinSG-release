// Package eval records anonymous task events and exports evaluation aggregates.
package eval

import (
	"errors"
	"regexp"
	"time"
	"unicode/utf8"
)

var (
	ErrUnavailable = errors.New("evaluation storage unavailable")
	taskOrder      = [...]string{"onboarding", "add_expense", "understand_budget", "get_advice", "add_expense_by_chat"}
	participantRE  = regexp.MustCompile(`^P[0-9]{2}$`)
	clientTimeRE   = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]+)?(Z|[+-]([01][0-9]|2[0-3]):[0-5][0-9])$`)
)

type ValidationError struct {
	Fields map[string]string
}

func (e *ValidationError) Error() string { return "invalid evaluation event fields" }

// EventInput is deliberately a whitelist. Unknown JSON fields, including
// amounts, notes and request-supplied identities, cannot reach persistence.
type EventInput struct {
	ParticipantCode string  `json:"participant_code"`
	Task            string  `json:"task"`
	Event           string  `json:"event"`
	Success         *bool   `json:"success"`
	ErrorCode       *string `json:"error_code"`
	ClientTS        *string `json:"client_ts"`
}

type Event struct {
	ID              int64     `json:"id"`
	ParticipantCode string    `json:"participant_code"`
	Task            string    `json:"task"`
	Event           string    `json:"event"`
	Success         *bool     `json:"success"`
	ErrorCode       *string   `json:"error_code"`
	ClientTS        *string   `json:"client_ts"`
	ReceivedAt      time.Time `json:"received_at"`
}

type TaskSummary struct {
	Task          string         `json:"task"`
	Participants  int            `json:"participants"`
	SuccessRate   *float64       `json:"success_rate"`
	MedianSeconds *float64       `json:"median_seconds"`
	P90Seconds    *float64       `json:"p90_seconds"`
	TopErrors     map[string]int `json:"top_errors"`
}

func validateEvent(input EventInput) (EventInput, error) {
	fields := make(map[string]string)
	if !participantRE.MatchString(input.ParticipantCode) {
		fields["participant_code"] = "must_match_P_followed_by_two_digits"
	}
	if !knownTask(input.Task) {
		fields["task"] = "must_be_a_supported_task"
	}
	if input.Event != "start" && input.Event != "end" {
		fields["event"] = "must_be_start_or_end"
	}
	if input.Event == "end" && input.Success == nil {
		fields["success"] = "required_for_end"
	}
	if input.ErrorCode != nil && (!utf8.ValidString(*input.ErrorCode) || utf8.RuneCountInString(*input.ErrorCode) > 32) {
		fields["error_code"] = "must_be_at_most_32_characters"
	}
	if input.ClientTS != nil {
		stamp, err := time.Parse(time.RFC3339Nano, *input.ClientTS)
		if err != nil || !clientTimeRE.MatchString(*input.ClientTS) {
			fields["client_ts"] = "must_be_RFC3339"
		} else {
			// Canonical RFC3339 needs at most 35 characters. Preserve the
			// supplied offset but never use this value for measured duration.
			value := stamp.Format(time.RFC3339Nano)
			input.ClientTS = &value
		}
	}
	if len(fields) != 0 {
		return EventInput{}, &ValidationError{Fields: fields}
	}
	return input, nil
}

func knownTask(task string) bool {
	for _, candidate := range taskOrder {
		if candidate == task {
			return true
		}
	}
	return false
}
