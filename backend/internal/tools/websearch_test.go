package tools

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"unichatgo/internal/llm"
)

type fakeSearch struct {
	name    string
	results []SearchResult
	err     error
	calls   int
	query   string
}

func (f *fakeSearch) Name() string { return f.name }

func (f *fakeSearch) Search(_ context.Context, query string, _ int) ([]SearchResult, error) {
	f.calls++
	f.query = query
	return f.results, f.err
}

func runWebSearch(t *testing.T, cfg WebSearchConfig, args string) (map[string]any, error) {
	t.Helper()
	registry := NewRegistry()
	if err := RegisterWebSearch(registry, cfg); err != nil {
		t.Fatalf("register: %v", err)
	}
	tool, ok := registry.Get("web_search")
	if !ok {
		t.Fatal("web_search not registered")
	}
	value, err := tool.Handler(context.Background(), UserContext{UserID: 7}, json.RawMessage(args))
	if err != nil {
		return nil, err
	}
	encoded, _ := json.Marshal(value)
	var out map[string]any
	if err := json.Unmarshal(encoded, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out, nil
}

func TestWebSearchFallsBackToNextProvider(t *testing.T) {
	google := &fakeSearch{name: "google", err: errors.New("quota")}
	ddg := &fakeSearch{name: "duckduckgo", results: []SearchResult{
		{Title: "  Student  fares ", URL: "https://example.com/fares", Snippet: "Concession\n pass $55"},
	}}
	out, err := runWebSearch(t, WebSearchConfig{Providers: []SearchProvider{google, ddg}}, `{"query":"MRT student concession Singapore"}`)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if google.calls != 1 || ddg.calls != 1 {
		t.Fatalf("calls google=%d ddg=%d", google.calls, ddg.calls)
	}
	if out["provider"] != "duckduckgo" || out["count"].(float64) != 1 {
		t.Fatalf("unexpected result: %v", out)
	}
	first := out["results"].([]any)[0].(map[string]any)
	if first["title"] != "Student fares" || first["snippet"] != "Concession pass $55" {
		t.Fatalf("whitespace not collapsed: %v", first)
	}
}

func TestWebSearchRejectsBadArguments(t *testing.T) {
	cfg := WebSearchConfig{Providers: []SearchProvider{&fakeSearch{name: "x"}}}
	for _, args := range []string{`{}`, `{"query":"  "}`, `{"query":"a","extra":1}`, `{"query":"` + strings.Repeat("a", 301) + `"}`} {
		_, err := runWebSearch(t, cfg, args)
		var argErr *ArgError
		if !errors.As(err, &argErr) {
			t.Fatalf("args %s: want ArgError, got %v", args, err)
		}
	}
}

func TestWebSearchAllProvidersFail(t *testing.T) {
	cfg := WebSearchConfig{Providers: []SearchProvider{&fakeSearch{name: "a", err: errors.New("boom")}}}
	if _, err := runWebSearch(t, cfg, `{"query":"hawker prices"}`); err == nil || strings.Contains(err.Error(), "boom") {
		t.Fatalf("want generic unavailable error, got %v", err)
	}
}

func TestWebSearchRateLimitPerUser(t *testing.T) {
	now := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	provider := &fakeSearch{name: "a", results: []SearchResult{{Title: "t", URL: "https://e.com"}}}
	registry := NewRegistry()
	if err := RegisterWebSearch(registry, WebSearchConfig{
		Providers: []SearchProvider{provider}, PerUserPerMinute: 2, Now: func() time.Time { return now },
	}); err != nil {
		t.Fatal(err)
	}
	tool, _ := registry.Get("web_search")
	call := func(user int64) error {
		_, err := tool.Handler(context.Background(), UserContext{UserID: user}, json.RawMessage(`{"query":"q"}`))
		return err
	}
	if call(1) != nil || call(1) != nil {
		t.Fatal("first two calls should pass")
	}
	if call(1) == nil {
		t.Fatal("third call in the same minute should be limited")
	}
	if call(2) != nil {
		t.Fatal("another user has its own budget")
	}
	now = now.Add(61 * time.Second)
	if call(1) != nil {
		t.Fatal("window should slide")
	}
}

func TestWebSearchReadsURLAsText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><head><title>Offer page</title><script>alert(1)</script></head>
<body><nav>menu</nav><main><h1>Student deal</h1><p>Lunch set  $4.50</p></main></body></html>`))
	}))
	defer server.Close()
	provider := &fakeSearch{name: "a"}
	out, err := runWebSearch(t, WebSearchConfig{Providers: []SearchProvider{provider}, Fetcher: server.Client()},
		`{"query":"`+server.URL+`/deal"}`)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if provider.calls != 0 {
		t.Fatal("URL query should not hit the search provider")
	}
	if out["title"] != "Offer page" || out["content"] != "Student deal Lunch set $4.50" {
		t.Fatalf("unexpected page: %v", out)
	}
}

func TestPublicFetchClientBlocksLoopback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("secret"))
	}))
	defer server.Close()
	provider := &fakeSearch{name: "a", results: []SearchResult{{Title: "fallback", URL: "https://e.com"}}}
	out, err := runWebSearch(t, WebSearchConfig{
		Providers: []SearchProvider{provider},
		Fetcher:   llm.NewPublicFetchClient(2*time.Second, 3),
	}, `{"query":"`+server.URL+`"}`)
	if err != nil {
		t.Fatalf("search fallback: %v", err)
	}
	if _, leaked := out["content"]; leaked || provider.calls != 1 {
		t.Fatalf("loopback page must not be read: %v", out)
	}
}

func TestRegisterWebSearchNeedsABackend(t *testing.T) {
	if err := RegisterWebSearch(NewRegistry(), WebSearchConfig{}); err == nil {
		t.Fatal("empty config must be rejected")
	}
}
