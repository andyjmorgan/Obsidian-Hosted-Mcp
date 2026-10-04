package server

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
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
	return New([]*vault.Vault{work, personal}, search.New("rg", nil), func() bool { return true })
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
		{"get_section", func() error { _, _, err := s.getSection(ctx, nil, getSectionInput{Vault: "Nope"}); return err }},
		{"replace_section", func() error { _, _, err := s.replaceSection(ctx, nil, replaceSectionInput{Vault: "Nope"}); return err }},
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
		{"restore_note", func() error {
			_, _, err := s.restoreNote(ctx, nil, restoreNoteInput{Vault: "Nope", Path: ".trash/x"})
			return err
		}},
		{"list_tasks", func() error { _, _, err := s.listTasks(ctx, nil, listTasksInput{Vault: "Nope"}); return err }},
		{"get_task", func() error { _, _, err := s.getTask(ctx, nil, taskRefInput{Vault: "Nope", ID: "x"}); return err }},
		{"create_task", func() error { _, _, err := s.createTask(ctx, nil, createTaskInput{Vault: "Nope"}); return err }},
		{"update_task", func() error { _, _, err := s.updateTask(ctx, nil, updateTaskInput{Vault: "Nope"}); return err }},
		{"complete_task", func() error { _, _, err := s.completeTask(ctx, nil, completeTaskInput{Vault: "Nope"}); return err }},
		{"reopen_task", func() error { _, _, err := s.reopenTask(ctx, nil, taskRefInput{Vault: "Nope"}); return err }},
		{"delete_task", func() error { _, _, err := s.deleteTask(ctx, nil, taskRefInput{Vault: "Nope"}); return err }},
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
	_, restored, err := s.restoreNote(ctx, nil, restoreNoteInput{Vault: "Personal", Path: ".trash/new.md"})
	if err != nil || !restored.OK || restored.RestoredTo != "new.md" {
		t.Fatalf("restoreNote: %+v %v", restored, err)
	}
	if _, _, err := s.restoreNote(ctx, nil, restoreNoteInput{Vault: "Personal", Path: "journal.md"}); err == nil {
		t.Error("restoreNote accepted a path outside .trash")
	}
	_, restored, err = s.restoreNote(ctx, nil, restoreNoteInput{Vault: "Personal", Path: ".trash/new.md"})
	if err == nil {
		t.Errorf("restoreNote succeeded on missing trash note: %+v", restored)
	}
	if _, out, err := s.deleteNote(ctx, nil, deleteNoteInput{Vault: "Personal", Path: "new.md"}); err != nil || !out.OK {
		t.Fatalf("re-deleteNote: %+v %v", out, err)
	}
	_, restored, err = s.restoreNote(ctx, nil, restoreNoteInput{Vault: "Personal", Path: ".trash/new.md", To: "archive/new.md"})
	if err != nil || restored.RestoredTo != "archive/new.md" {
		t.Fatalf("restoreNote with to: %+v %v", restored, err)
	}
	if _, out, err := s.deleteNote(ctx, nil, deleteNoteInput{Vault: "Personal", Path: "archive/new.md"}); err != nil || !out.OK {
		t.Fatalf("re-deleteNote 2: %+v %v", out, err)
	}
	_, del, err = s.deleteNote(ctx, nil, deleteNoteInput{Vault: "Personal", Path: ".trash/new.md", Permanent: true})
	if err != nil || del.TrashedTo != "" {
		t.Fatalf("permanent deleteNote: %+v %v", del, err)
	}
	if _, err := os.Stat(filepath.Join(v.Root(), ".trash", "new.md")); !os.IsNotExist(err) {
		t.Error("permanent delete left the file behind")
	}
}

func TestHealthEndpoints(t *testing.T) {
	s := newTestServer(t)
	ready := true
	s.syncReady = func() bool { return ready }
	ts := httptest.NewServer(s.Handler(AuthConfig{StaticToken: "secret"}))
	defer ts.Close()

	for _, path := range []string{"/livez", "/readyz", "/healthz"} {
		res, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Errorf("%s status = %d, want 200", path, res.StatusCode)
		}
	}

	ready = false
	for path, want := range map[string]int{
		"/livez":   http.StatusOK,
		"/readyz":  http.StatusServiceUnavailable,
		"/healthz": http.StatusServiceUnavailable,
	} {
		res, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != want {
			t.Errorf("%s status = %d, want %d", path, res.StatusCode, want)
		}
	}
}

func TestBearerAuth(t *testing.T) {
	s := newTestServer(t)
	ts := httptest.NewServer(s.Handler(AuthConfig{StaticToken: "secret"}))
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
	ts := httptest.NewServer(s.Handler(AuthConfig{StaticToken: "secret"}))
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
		"append_note", "complete_task", "create_note", "create_task", "delete_note", "delete_task",
		"edit_note", "get_section", "get_task", "list_notes", "list_tasks", "list_vaults",
		"move_note", "read_note", "reopen_task", "replace_section", "restore_note",
		"search_notes", "update_task",
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
	ts := httptest.NewServer(s.Handler(AuthConfig{StaticToken: "secret"}))
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

func oidcTestAuth(verify func(ctx context.Context, token string) (*auth.TokenInfo, error)) AuthConfig {
	return AuthConfig{
		OIDC: &OIDCAuth{
			Verify:    verify,
			Issuer:    "https://idp.example.com/realms/lab",
			Scopes:    []string{"openid", "profile"},
			PublicURL: "https://obsidian.example.com",
		},
	}
}

func fakeVerify(t *testing.T) func(ctx context.Context, token string) (*auth.TokenInfo, error) {
	t.Helper()
	return func(_ context.Context, token string) (*auth.TokenInfo, error) {
		if token == "good-jwt" {
			return &auth.TokenInfo{UserID: "user-1", Expiration: time.Now().Add(time.Hour)}, nil
		}
		return nil, fmt.Errorf("%w: not good-jwt", auth.ErrInvalidToken)
	}
}

func TestProtectedResourceMetadata(t *testing.T) {
	s := newTestServer(t)
	ts := httptest.NewServer(s.Handler(oidcTestAuth(fakeVerify(t))))
	defer ts.Close()

	res, err := http.Get(ts.URL + "/.well-known/oauth-protected-resource")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("metadata status = %d", res.StatusCode)
	}
	var meta struct {
		Resource             string   `json:"resource"`
		AuthorizationServers []string `json:"authorization_servers"`
		ScopesSupported      []string `json:"scopes_supported"`
	}
	if err := json.NewDecoder(res.Body).Decode(&meta); err != nil {
		t.Fatal(err)
	}
	if meta.Resource != "https://obsidian.example.com" {
		t.Errorf("resource = %q", meta.Resource)
	}
	if len(meta.AuthorizationServers) != 1 || meta.AuthorizationServers[0] != "https://idp.example.com/realms/lab" {
		t.Errorf("authorization_servers = %v", meta.AuthorizationServers)
	}
	if fmt.Sprint(meta.ScopesSupported) != fmt.Sprint([]string{"openid", "profile"}) {
		t.Errorf("scopes_supported = %v", meta.ScopesSupported)
	}
}

func TestNoMetadataWithoutOIDC(t *testing.T) {
	s := newTestServer(t)
	ts := httptest.NewServer(s.Handler(AuthConfig{StaticToken: "secret"}))
	defer ts.Close()
	res, err := http.Get(ts.URL + "/.well-known/oauth-protected-resource")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	// Without OIDC there is no OAuth flow to advertise; the path falls
	// through to the bearer-protected MCP mux and is rejected.
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("metadata status = %d, want 401", res.StatusCode)
	}
}

func TestOIDCBearer(t *testing.T) {
	s := newTestServer(t)
	cfg := oidcTestAuth(fakeVerify(t))
	cfg.StaticToken = "api-key" // both modes enabled side by side
	ts := httptest.NewServer(s.Handler(cfg))
	defer ts.Close()

	post := func(header string) *http.Response {
		req, err := http.NewRequest(http.MethodPost, ts.URL, strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { res.Body.Close() })
		return res
	}

	if res := post("Bearer good-jwt"); res.StatusCode == http.StatusUnauthorized {
		t.Error("OIDC token rejected")
	}
	if res := post("Bearer api-key"); res.StatusCode == http.StatusUnauthorized {
		t.Error("static API key rejected when OIDC is enabled")
	}
	res := post("Bearer bad-jwt")
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("bad token status = %d, want 401", res.StatusCode)
	}
	if got := res.Header.Get("WWW-Authenticate"); !strings.Contains(got, "resource_metadata=") ||
		!strings.Contains(got, "https://obsidian.example.com/.well-known/oauth-protected-resource") {
		t.Errorf("WWW-Authenticate = %q, want resource_metadata challenge", got)
	}
	if res := post(""); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("missing header status = %d, want 401", res.StatusCode)
	}
}

func TestOIDCOnlyRejectsStaticToken(t *testing.T) {
	s := newTestServer(t)
	ts := httptest.NewServer(s.Handler(oidcTestAuth(fakeVerify(t))))
	defer ts.Close()
	req, err := http.NewRequest(http.MethodPost, ts.URL, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer some-api-key")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 (no static token configured)", res.StatusCode)
	}
}

func TestSectionToolsOverHTTP(t *testing.T) {
	s := newTestServer(t)
	ts := httptest.NewServer(s.Handler(AuthConfig{StaticToken: "secret"}))
	defer ts.Close()
	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "section-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL, HTTPClient: &http.Client{Transport: authTransport{token: "secret"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	call := func(name string, args map[string]any, wantError bool) *mcp.CallToolResult {
		t.Helper()
		out, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		if out.IsError != wantError {
			t.Fatalf("%s: %+v", name, out)
		}
		return out
	}
	args := map[string]any{"vault": "Work", "path": "note.md", "heading_path": []string{"Note"}}
	out := call("get_section", args, false)
	data, err := json.Marshal(out.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var section vault.SectionResult
	if err := json.Unmarshal(data, &section); err != nil {
		t.Fatal(err)
	}
	if section.ReadResult == nil || section.Content != "\nhello world\n" || section.Level != 1 || !slices.Equal(section.HeadingPath, []string{"Note"}) {
		t.Fatalf("section = %s", data)
	}
	args["content"] = "updated\n"
	call("replace_section", args, false)
	delete(args, "content")
	out = call("get_section", args, false)
	if !strings.Contains(out.Content[0].(*mcp.TextContent).Text, "updated") {
		t.Fatalf("%+v", out)
	}
	args["heading_path"] = []string{"Missing"}
	call("get_section", args, true)
	args["content"] = "oops"
	call("replace_section", args, true)
	delete(args, "content")
	call("replace_section", args, true) // required content is checked by the schema
	delete(args, "heading_path")
	call("get_section", args, true)
}

// TestTaskToolsOverHTTP walks a reminder through its life over a real MCP
// session: create, nest a subtask, list, update, refuse to close early,
// close in order, reopen and delete — and checks the Markdown that lands
// in the reminders note follows the documented convention.
func TestTaskToolsOverHTTP(t *testing.T) {
	s := newTestServer(t)
	s.TasksFile = "Planning/Reminders.md"
	ts := httptest.NewServer(s.Handler(AuthConfig{StaticToken: "secret"}))
	defer ts.Close()
	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "task-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL, HTTPClient: &http.Client{Transport: authTransport{token: "secret"}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	call := func(name string, args map[string]any, wantError bool) map[string]any {
		t.Helper()
		out, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		if out.IsError != wantError {
			t.Fatalf("%s(%v): %+v", name, args, out.Content)
		}
		if wantError {
			return nil
		}
		data, err := json.Marshal(out.StructuredContent)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	task := func(m map[string]any) map[string]any { return m["task"].(map[string]any) }
	ctxText := "Agreed in the 2026-10-04 planning call; the registrar notice is in [[Domains]]."

	if out := call("list_tasks", map[string]any{"vault": "Work"}, false); len(out["tasks"].([]any)) != 0 {
		t.Fatalf("fresh store not empty: %v", out)
	}
	call("create_task", map[string]any{"vault": "Work", "title": "Renew domain", "context": "too short"}, true)
	parent := task(call("create_task", map[string]any{
		"vault": "Work", "title": "Renew domain", "context": ctxText, "related": []string{"Domains"},
		"source": "Planning call 2026-10-04", "due": "2026-10-20", "due_time": "09:00",
	}, false))
	pid := parent["id"].(string)
	if !strings.HasPrefix(pid, "rem-") || parent["section"] != "Inbox" || parent["due"] != "2026-10-20" {
		t.Fatalf("parent = %v", parent)
	}
	child := task(call("create_task", map[string]any{
		"vault": "Work", "title": "Check auto-renew flag", "context": ctxText, "parent_id": pid,
	}, false))
	cid := child["id"].(string)
	if child["parent_id"] != pid || child["depth"].(float64) != 1 {
		t.Fatalf("child = %v", child)
	}

	got := call("get_task", map[string]any{"vault": "Work", "id": pid}, false)
	if task(got)["open_subtasks"].(float64) != 1 || len(got["subtasks"].([]any)) != 1 {
		t.Fatalf("get_task = %v", got)
	}
	call("get_task", map[string]any{"vault": "Work", "id": "rem-nope"}, true)

	listed := call("list_tasks", map[string]any{"vault": "Work", "status": "open"}, false)["tasks"].([]any)
	if len(listed) != 2 {
		t.Fatalf("list = %v", listed)
	}
	call("list_tasks", map[string]any{"vault": "Work", "status": "bogus"}, true)

	updated := task(call("update_task", map[string]any{"vault": "Work", "id": cid, "title": "Check the auto-renew flag", "clear_due": true}, false))
	if updated["title"] != "Check the auto-renew flag" {
		t.Fatalf("update = %v", updated)
	}
	call("update_task", map[string]any{"vault": "Work", "id": cid, "due": "nope"}, true)

	call("complete_task", map[string]any{"vault": "Work", "id": pid, "resolution": "Renewed for 2 years"}, true)
	call("complete_task", map[string]any{"vault": "Work", "id": cid, "resolution": ""}, true)
	call("complete_task", map[string]any{"vault": "Work", "id": cid, "resolution": "Flag was off; left off deliberately", "date": "2026-10-05"}, false)
	done := task(call("complete_task", map[string]any{"vault": "Work", "id": pid, "resolution": "Renewed for 2 years, receipt in [[Billing]]"}, false))
	if done["done"] != true || !strings.HasSuffix(done["closed"].(string), " — Renewed for 2 years, receipt in [[Billing]]") {
		t.Fatalf("complete = %v", done)
	}
	if open := call("list_tasks", map[string]any{"vault": "Work"}, false)["tasks"].([]any); len(open) != 0 {
		t.Fatalf("open after completion: %v", open)
	}
	if all := call("list_tasks", map[string]any{"vault": "Work", "status": "done", "section": "inbox"}, false)["tasks"].([]any); len(all) != 2 {
		t.Fatalf("done list = %v", all)
	}

	note, err := os.ReadFile(filepath.Join(s.vaults["Work"].Root(), "Planning", "Reminders.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"## Inbox\n\n- [x] **Renew domain** 📅 2026-10-20 ⏰ 09:00 ^" + pid + "\n",
		"  - **Context:** " + ctxText + "\n  - **Related:** [[Domains]]\n  - **Source:** Planning call 2026-10-04\n",
		"  - **Closed:** " + time.Now().Format("2006-01-02") + " — Renewed for 2 years, receipt in [[Billing]]\n",
		"  - [x] **Check the auto-renew flag** ^" + cid + "\n    - **Context:** " + ctxText + "\n    - **Closed:** 2026-10-05 — Flag was off; left off deliberately\n",
	} {
		if !strings.Contains(string(note), want) {
			t.Errorf("note missing %q:\n%s", want, note)
		}
	}

	reopened := task(call("reopen_task", map[string]any{"vault": "Work", "id": pid}, false))
	if reopened["done"] != false {
		t.Fatalf("reopen = %v", reopened)
	}
	call("reopen_task", map[string]any{"vault": "Work", "id": "rem-nope"}, true)
	call("delete_task", map[string]any{"vault": "Work", "id": "rem-nope"}, true)
	if out := call("delete_task", map[string]any{"vault": "Work", "id": pid}, false); out["ok"] != true {
		t.Fatalf("delete = %v", out)
	}
	if all := call("list_tasks", map[string]any{"vault": "Work", "status": "all"}, false)["tasks"].([]any); len(all) != 0 {
		t.Fatalf("tasks left after delete: %v", all)
	}
}
