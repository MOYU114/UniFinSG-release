package agentv2

import (
	"encoding/json"
	"fmt"
	"net/url"
)

// summarizeToolResult renders the one-line human summary sent in the
// tool_result SSE event. Full results stay on the server.
func summarizeToolResult(name string, payload json.RawMessage) string {
	var generic map[string]any
	if err := json.Unmarshal(payload, &generic); err != nil {
		return "Ran " + name
	}
	if message, ok := generic["error"].(string); ok && message != "" {
		return fmt.Sprintf("%s could not run: %s", name, message)
	}
	switch name {
	case "list_categories":
		return "Listed the 6 spending categories"
	case "get_budget_snapshot":
		if month, ok := generic["month"].(string); ok && month != "" {
			return fmt.Sprintf("Checked the %s budget snapshot", month)
		}
		return "Checked the budget snapshot"
	case "list_transactions":
		count := 0
		if value, ok := generic["count"].(float64); ok {
			count = int(value)
		}
		month, _ := generic["month"].(string)
		if month != "" {
			return fmt.Sprintf("Listed %d transactions for %s", count, month)
		}
		return fmt.Sprintf("Listed %d transactions", count)
	case "web_search":
		if page, ok := generic["url"].(string); ok && page != "" {
			if parsed, err := url.Parse(page); err == nil && parsed.Host != "" {
				return "Read " + parsed.Host
			}
			return "Read a web page"
		}
		if query, ok := generic["query"].(string); ok && query != "" {
			return fmt.Sprintf("Searched the web for %q", query)
		}
		return "Searched the web"
	default:
		return "Ran " + name
	}
}
