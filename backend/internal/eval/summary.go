package eval

import (
	"math"
	"sort"
	"time"
)

type attempt struct {
	started   time.Time
	completed bool
	success   bool
	seconds   float64
	errorCode string
}

// summarize uses each participant's latest started attempt for a task. Starts
// replace earlier attempts; the first following end consumes the pending start.
// Unpaired/duplicate ends remain available in Events but cannot inflate counts.
func summarize(events []Event) []TaskSummary {
	ordered := append([]Event(nil), events...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].ReceivedAt.Equal(ordered[j].ReceivedAt) {
			return ordered[i].ID < ordered[j].ID
		}
		return ordered[i].ReceivedAt.Before(ordered[j].ReceivedAt)
	})
	latest := make(map[string]map[string]*attempt, len(taskOrder))
	for _, event := range ordered {
		if !knownTask(event.Task) {
			continue
		}
		participants := latest[event.Task]
		if participants == nil {
			participants = make(map[string]*attempt)
			latest[event.Task] = participants
		}
		switch event.Event {
		case "start":
			participants[event.ParticipantCode] = &attempt{started: event.ReceivedAt}
		case "end":
			current := participants[event.ParticipantCode]
			if current == nil || current.completed || event.Success == nil {
				continue
			}
			current.completed, current.success = true, *event.Success
			current.seconds = event.ReceivedAt.Sub(current.started).Seconds()
			if !current.success && event.ErrorCode != nil {
				current.errorCode = *event.ErrorCode
			}
		}
	}
	results := make([]TaskSummary, 0, len(taskOrder))
	for _, task := range taskOrder {
		participants := latest[task]
		result := TaskSummary{Task: task, Participants: len(participants), TopErrors: make(map[string]int)}
		successes := 0
		var durations []float64
		for _, current := range participants {
			if !current.completed {
				continue
			}
			if current.success {
				successes++
			} else if current.errorCode != "" {
				result.TopErrors[current.errorCode]++
			}
			durations = append(durations, current.seconds)
		}
		if result.Participants > 0 {
			rate := float64(successes) / float64(result.Participants)
			result.SuccessRate = &rate
		}
		if len(durations) > 0 {
			sort.Float64s(durations)
			middle := len(durations) / 2
			median := durations[middle]
			if len(durations)%2 == 0 {
				median = (durations[middle-1] + median) / 2
			}
			p90 := durations[int(math.Ceil(0.9*float64(len(durations))))-1]
			result.MedianSeconds, result.P90Seconds = &median, &p90
		}
		results = append(results, result)
	}
	return results
}
