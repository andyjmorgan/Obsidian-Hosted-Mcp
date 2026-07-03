package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/andyjmorgan/obsidian-hosted-mcp/internal/search"
	"github.com/andyjmorgan/obsidian-hosted-mcp/internal/vault"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	work := vault.New("Work", t.TempDir())
	personal := vault.New("Personal", t.TempDir())
	write(t, work, "note.md", "# Note\n\nhello world\n")
	write(t, work, "projects/plan.md", "TODO: ship the MCP server\n")
	write(t, personal, "journal.md", "quiet day\n")
	return New([]*vault.Vault{work, personal}, search.New("rg", nil))
}

func write(t *testing.T, v *vault.Vault, rel, content string) {
	t.Helper()
	abs := filepath.Join(v.Root(), filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestListVaults(t *testing.T) {
	s := newTestServer(t)
	_, out, err := s.listVaults(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(out.Vaults, []string{"Personal", "Work"}) {
		t.Errorf("Vaults = %v", out.Vaults)
	}
}

func TestUnknownVaultRejectedByEveryTool(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()
	checks := []struct {
		tool string
		call func() error
	}{
		{"list_notes", func() error { _, _, err := s.listNotes(ctx, nil, listNotesInput{Vault: "Nope"}); return err }},
		{"read_note", func() error { _, _, err := s.readNote(ctx, nil, readNoteInput{Vault: "Nope", Path: "x"}); return err }},
		{"search_notes", func() error {
			_, _, err := s.searchNotes(ctx, nil, searchNotesInput{Vault: "Nope", Query: "x"})
			return err
		}},
		{"create_note", func() error {
			_, _, err := s.createNote(ctx, nil, writeNoteInput{Vault: "Nope", Path: "x"})
			return err
		}},
		{"append_note", func() error {
			_, _, err := s.appendNote(ctx, nil, writeNoteInput{Vault: "Nope", Path: "x"})
			return err
		}},
		{"edit_note", func() error {
			_, _, err := s.editNote(ctx, nil, editNoteInput{Vault: "Nope", Path: "x", Find: "a"})
			return err
		}},
		{"move_note", func() error {
			_, _, err := s.moveNote(ctx, nil, moveNoteInput{Vault: "Nope", Path: "x", NewPath: "y"})
			return err
		}},
		{"delete_note", func() error {
			_, _, err := s.deleteNote(ctx, nil, deleteNoteInput{Vault: "Nope", Path: "x"})
			return err
		}},
	}
	for _, c := range checks {
		if err := c.call(); err == nil || !strings.Contains(err.Error(), "unknown vault") {
			t.Errorf("%s: err = %v, want unknown vault", c.tool, err)
		}
	}
}

func TestListNotes(t *testing.T) {
	s := newTestServer(t)
	_, out, err := s.listNotes(context.Background(), nil, listNotesInput{Vault: "Work", Recursive: true})
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, e := range out.Entries {
		paths = append(paths, e.Path)
	}
	want := []string{"note.md", "projects", "projects/plan.md"}
	if !slices.Equal(paths, want) {
		t.Errorf("paths = %v, want %v", paths, want)
	}

	if _, _, err := s.listNotes(context.Background(), nil, listNotesInput{Vault: "Work", Dir: "missing"}); err == nil {
		t.Error("listNotes succeeded on missing dir")
	}
}

func TestReadNote(t *testing.T) {
	s := newTestServer(t)
	_, out, err := s.readNote(context.Background(), nil, readNoteInput{Vault: "Work", Path: "note.md"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Content, "hello world") || out.Truncated {
		t.Errorf("out = %+v", out)
	}
	if _, _, err := s.readNote(context.Background(), nil, readNoteInput{Vault: "Work", Path: "missing.md"}); err == nil {
		t.Error("readNote succeeded on missing note")
	}
}

func TestSearchNotes(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("rg not installed")
	}
	s := newTestServer(t)
	_, out, err := s.searchNotes(context.Background(), nil, searchNotesInput{Vault: "Work", Query: "todo"})
	if err != nil {
		t.Fatal(err)
	}
	if out.TotalMatches != 1 || out.Files[0].Path != "projects/plan.md" {
		t.Errorf("out = %+v", out)
	}
	if _, _, err := s.searchNotes(context.Background(), nil, searchNotesInput{Vault: "Work", Query: ""}); err == nil {
		t.Error("searchNotes accepted empty query")
	}
}

func TestWriteTools(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()
	v := s.vaults["Personal"]

	if _, out, err := s.createNote(ctx, nil, writeNoteInput{Vault: "Personal", Path: "new.md", Content: "fresh"}); err != nil || !out.OK {
		t.Fatalf("createNote: %+v %v", out, err)
	}
	if _, _, err := s.createNote(ctx, nil, writeNoteInput{Vault: "Personal", Path: "new.md", Content: "dup"}); err == nil {
		t.Error("createNote overwrote existing note")
	}

	if _, out, err := s.appendNote(ctx, nil, writeNoteInput{Vault: "Personal", Path: "new.md", Content: " more"}); err != nil || !out.OK {
		t.Fatalf("appendNote: %+v %v", out, err)
	}
	if _, _, err := s.appendNote(ctx, nil, writeNoteInput{Vault: "Personal", Path: "../escape.md", Content: "x"}); err == nil {
		t.Error("appendNote escaped the vault root")
	}

	_, edited, err := s.editNote(ctx, nil, editNoteInput{Vault: "Personal", Path: "new.md", Find: "fresh", Replace: "stale"})
	if err != nil || edited.Replacements != 1 {
		t.Fatalf("editNote: %+v %v", edited, err)
	}
	if _, _, err := s.editNote(ctx, nil, editNoteInput{Vault: "Personal", Path: "new.md", Find: "absent", Replace: "x"}); err == nil {
		t.Error("editNote succeeded on absent text")
	}

	if _, out, err := s.moveNote(ctx, nil, moveNoteInput{Vault: "Personal", Path: "new.md", NewPath: "archive/new.md"}); err != nil || !out.OK {
		t.Fatalf("moveNote: %+v %v", out, err)
	}
	if _, _, err := s.moveNote(ctx, nil, moveNoteInput{Vault: "Personal", Path: "archive/new.md", NewPath: "journal.md"}); err == nil {
		t.Error("moveNote overwrote existing destination")
	}

	_, del, err := s.deleteNote(ctx, nil, deleteNoteInput{Vault: "Personal", Path: "archive/new.md"})
	if err != nil || !del.OK || del.TrashedTo != ".trash/new.md" {
		t.Fatalf("deleteNote: %+v %v", del, err)
	}
	if _, _, err := s.deleteNote(ctx, nil, deleteNoteInput{Vault: "Personal", Path: "archive/new.md"}); err == nil {
		t.Error("deleteNote succeeded on missing note")
	}
	_, del, err = s.deleteNote(ctx, nil, deleteNoteInput{Vault: "Personal", Path: ".trash/new.md", Permanent: true})
	if err != nil || del.TrashedTo != "" {
		t.Fatalf("permanent deleteNote: %+v %v", del, err)
	}
	if _, err := os.Stat(filepath.Join(v.Root(), ".trash", "new.md")); !os.IsNotExist(err) {
		t.Error("permanent delete left the file behind")
	}
}

func TestHealthz(t *testing.T) {
	s := newTestServer(t)
	ts := httptest.NewServer(s.Handler("secret"))
	defer ts.Close()

	res, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("healthz status = %d", res.StatusCode)
	}
}

func TestBearerAuth(t *testing.T) {
	s := newTestServer(t)
	ts := httptest.NewServer(s.Handler("secret"))
	defer ts.Close()

	for _, tt := range []struct {
		name   string
		header string
		want   int
	}{
		{"missing header", "", http.StatusUnauthorized},
		{"wrong scheme", "Basic secret", http.StatusUnauthorized},
		{"wrong token", "Bearer nope", http.StatusUnauthorized},
		{"token prefix", "Bearer secretx", http.StatusUnauthorized},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, ts.URL, strings.NewReader("{}"))
			if err != nil {
				t.Fatal(err)
			}
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			res.Body.Close()
			if res.StatusCode != tt.want {
				t.Errorf("status = %d, want %d", res.StatusCode, tt.want)
			}
		})
	}
}

// authTransport injects the bearer token into every request.
type authTransport struct{ token string }

func (a authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", "Bearer "+a.token)
	return http.DefaultTransport.RoundTrip(req)
}

// TestEndToEndOverHTTP drives the server through a real MCP client session
// over the streamable HTTP transport, including auth.
func TestEndToEndOverHTTP(t *testing.T) {
	s := newTestServer(t)
	ts := httptest.NewServer(s.Handler("secret"))
	defer ts.Close()

	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   ts.URL,
		HTTPClient: &http.Client{Transport: authTransport{token: "secret"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	slices.Sort(names)
	want := []string{
		"append_note", "create_note", "delete_note", "edit_note",
		"list_notes", "list_vaults", "move_note", "read_note", "search_notes",
	}
	if !slices.Equal(names, want) {
		t.Errorf("tools = %v, want %v", names, want)
	}

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "read_note",
		Arguments: map[string]any{"vault": "Work", "path": "note.md"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("read_note errored: %+v", res.Content)
	}
	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok || !strings.Contains(text.Text, "hello world") {
		t.Errorf("content = %+v", res.Content)
	}

	res, err = session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "read_note",
		Arguments: map[string]any{"vault": "Work", "path": "../escape.md"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Error("path escape did not produce a tool error")
	}
}

// TestEndToEndRejectsBadToken confirms an unauthenticated MCP client cannot
// establish a session.
func TestEndToEndRejectsBadToken(t *testing.T) {
	s := newTestServer(t)
	ts := httptest.NewServer(s.Handler("secret"))
	defer ts.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:   ts.URL,
		HTTPClient: &http.Client{Transport: authTransport{token: "wrong"}},
		MaxRetries: -1,
	}, nil)
	if err == nil {
		session.Close()
		t.Fatal("client connected with a bad token")
	}
}
