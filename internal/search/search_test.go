package search

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func fakeRun(stdout, stderr string, exitCode int, err error) RunFunc {
	return func(context.Context, string, string, ...string) ([]byte, []byte, int, error) {
		return []byte(stdout), []byte(stderr), exitCode, err
	}
}

const sampleOutput = `{"type":"begin","data":{"path":{"text":"./a.md"}}}
{"type":"context","data":{"path":{"text":"./a.md"},"lines":{"text":"before\n"},"line_number":1}}
{"type":"match","data":{"path":{"text":"./a.md"},"lines":{"text":"hit one\n"},"line_number":2}}
{"type":"end","data":{"path":{"text":"./a.md"}}}
{"type":"begin","data":{"path":{"text":"./b.md"}}}
{"type":"match","data":{"path":{"text":"./b.md"},"lines":{"text":"hit two\n"},"line_number":5}}
{"type":"summary","data":{}}
`

func TestSearchParsesMatchesAndContext(t *testing.T) {
	s := New("rg", fakeRun(sampleOutput, "", 0, nil))
	res, err := s.Search(context.Background(), "/vault", Options{Query: "hit"})
	if err != nil {
		t.Fatal(err)
	}
	if res.TotalMatches != 2 || res.Truncated {
		t.Fatalf("got %+v", res)
	}
	if len(res.Files) != 2 || res.Files[0].Path != "a.md" || res.Files[1].Path != "b.md" {
		t.Fatalf("files = %+v", res.Files)
	}
	wantA := []Line{
		{Number: 1, Text: "before", Match: false},
		{Number: 2, Text: "hit one", Match: true},
	}
	if !slices.Equal(res.Files[0].Lines, wantA) {
		t.Errorf("a.md lines = %+v", res.Files[0].Lines)
	}
	if !slices.Equal(res.Files[1].Lines, []Line{{Number: 5, Text: "hit two", Match: true}}) {
		t.Errorf("b.md lines = %+v", res.Files[1].Lines)
	}
}

func TestSearchTruncatesAtMaxResults(t *testing.T) {
	s := New("rg", fakeRun(sampleOutput, "", 0, nil))
	res, err := s.Search(context.Background(), "/vault", Options{Query: "hit", MaxResults: 1})
	if err != nil {
		t.Fatal(err)
	}
	if res.TotalMatches != 1 || !res.Truncated {
		t.Fatalf("got %+v", res)
	}
	if len(res.Files) != 1 {
		t.Fatalf("files = %+v", res.Files)
	}
}

func TestSearchMaxResultsCeiling(t *testing.T) {
	var events strings.Builder
	for range MaxResultsCeiling + 10 {
		events.WriteString(`{"type":"match","data":{"path":{"text":"./a.md"},"lines":{"text":"hit\n"},"line_number":1}}` + "\n")
	}
	s := New("rg", fakeRun(events.String(), "", 0, nil))
	res, err := s.Search(context.Background(), "/vault", Options{Query: "hit", MaxResults: 10000})
	if err != nil {
		t.Fatal(err)
	}
	if res.TotalMatches != MaxResultsCeiling || !res.Truncated {
		t.Fatalf("got TotalMatches=%d Truncated=%v", res.TotalMatches, res.Truncated)
	}
}

func TestSearchNoMatches(t *testing.T) {
	s := New("rg", fakeRun("", "", 1, nil))
	res, err := s.Search(context.Background(), "/vault", Options{Query: "absent"})
	if err != nil {
		t.Fatal(err)
	}
	if res.TotalMatches != 0 || len(res.Files) != 0 || res.Truncated {
		t.Fatalf("got %+v", res)
	}
	out, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte(`"files":[]`)) {
		t.Fatalf("zero-match result must marshal files as [], not null: %s", out)
	}
}

func TestSearchEmptyQuery(t *testing.T) {
	s := New("rg", fakeRun("", "", 0, nil))
	for _, q := range []string{"", "   "} {
		if _, err := s.Search(context.Background(), "/vault", Options{Query: q}); err == nil {
			t.Errorf("Search accepted query %q", q)
		}
	}
}

func TestSearchRipgrepError(t *testing.T) {
	s := New("rg", fakeRun("", "regex parse error", 2, nil))
	_, err := s.Search(context.Background(), "/vault", Options{Query: "("})
	if err == nil || !strings.Contains(err.Error(), "regex parse error") {
		t.Errorf("err = %v", err)
	}
}

func TestSearchRunFailure(t *testing.T) {
	s := New("rg", fakeRun("", "", 0, errors.New("binary not found")))
	if _, err := s.Search(context.Background(), "/vault", Options{Query: "x"}); err == nil {
		t.Error("Search succeeded despite run failure")
	}
}

func TestSearchMalformedOutput(t *testing.T) {
	s := New("rg", fakeRun("not json\n", "", 0, nil))
	if _, err := s.Search(context.Background(), "/vault", Options{Query: "x"}); err == nil {
		t.Error("Search accepted malformed output")
	}
}

func TestSearchArgumentConstruction(t *testing.T) {
	var gotArgs []string
	run := func(_ context.Context, dir, name string, args ...string) ([]byte, []byte, int, error) {
		if dir != "/vault" || name != "rg" {
			t.Errorf("dir=%q name=%q", dir, name)
		}
		gotArgs = args
		return nil, nil, 1, nil
	}
	s := New("rg", run)
	_, err := s.Search(context.Background(), "/vault", Options{
		Query:         "todo",
		Glob:          "*.md",
		CaseSensitive: true,
		ContextLines:  2,
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(gotArgs, " ")
	for _, want := range []string{"--json", "--no-ignore", "--context 2", "--glob *.md", "--regexp todo"} {
		if !strings.Contains(joined, want) {
			t.Errorf("args %q missing %q", joined, want)
		}
	}
	if strings.Contains(joined, "--ignore-case") {
		t.Errorf("args %q include --ignore-case despite CaseSensitive", joined)
	}

	_, err = s.Search(context.Background(), "/vault", Options{Query: "todo"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(gotArgs, " "), "--ignore-case") {
		t.Error("default search is not case-insensitive")
	}
}

// TestSearchIntegration runs the real ripgrep binary over a temp vault.
func TestSearchIntegration(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("rg not installed")
	}
	root := t.TempDir()
	write := func(rel, content string) {
		t.Helper()
		abs := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("projects/roadmap.md", "# Roadmap\n\n- [ ] Ship MCP server\n- [x] Write TESTS\n")
	write("daily/today.md", "Reviewed the mcp server plans.\n")
	write(".obsidian/app.json", `{"mcp": "should not match"}`)
	write(".trash/deleted.md", "mcp mention in trash\n")

	s := New("rg", nil)

	res, err := s.Search(context.Background(), root, Options{Query: "mcp server"})
	if err != nil {
		t.Fatal(err)
	}
	if res.TotalMatches != 2 {
		t.Fatalf("TotalMatches = %d, want 2 (hidden dirs must be excluded): %+v", res.TotalMatches, res)
	}
	paths := []string{res.Files[0].Path, res.Files[1].Path}
	slices.Sort(paths)
	if paths[0] != "daily/today.md" || paths[1] != "projects/roadmap.md" {
		t.Errorf("paths = %v", paths)
	}

	res, err = s.Search(context.Background(), root, Options{Query: "TESTS", CaseSensitive: true, Glob: "projects/**"})
	if err != nil {
		t.Fatal(err)
	}
	if res.TotalMatches != 1 || res.Files[0].Path != "projects/roadmap.md" {
		t.Errorf("case-sensitive glob search: %+v", res)
	}

	res, err = s.Search(context.Background(), root, Options{Query: "Ship", ContextLines: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) != 1 || len(res.Files[0].Lines) != 3 {
		t.Errorf("context search: %+v", res)
	}

	if _, err := s.Search(context.Background(), root, Options{Query: "(unclosed"}); err == nil {
		t.Error("invalid regex did not error")
	}
}

func TestSearchBinaryMissing(t *testing.T) {
	s := New("this-binary-does-not-exist-2a15", nil)
	if _, err := s.Search(context.Background(), t.TempDir(), Options{Query: "x"}); err == nil {
		t.Error("Search succeeded without the ripgrep binary")
	}
}

func TestSearchSkipsBlankOutputLines(t *testing.T) {
	out := "\n" + `{"type":"match","data":{"path":{"text":"./a.md"},"lines":{"text":"hit\n"},"line_number":1}}` + "\n\n"
	s := New("rg", fakeRun(out, "", 0, nil))
	res, err := s.Search(context.Background(), "/vault", Options{Query: "hit"})
	if err != nil {
		t.Fatal(err)
	}
	if res.TotalMatches != 1 {
		t.Errorf("TotalMatches = %d", res.TotalMatches)
	}
}

func TestSearchOversizedOutputLine(t *testing.T) {
	huge := `{"type":"context","data":{"path":{"text":"./a.md"},"lines":{"text":"` +
		strings.Repeat("x", 5*1024*1024) + `"},"line_number":1}}`
	s := New("rg", fakeRun(huge, "", 0, nil))
	if _, err := s.Search(context.Background(), "/vault", Options{Query: "x"}); err == nil {
		t.Error("Search accepted a line beyond the scanner limit")
	}
}
