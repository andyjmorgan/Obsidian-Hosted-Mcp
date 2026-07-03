// Package server exposes vault operations as MCP tools over streamable
// HTTP, protected by a static bearer token.
package server

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/http"
	"sort"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/andyjmorgan/obsidian-hosted-mcp/internal/search"
	"github.com/andyjmorgan/obsidian-hosted-mcp/internal/vault"
)

// Version is the server version reported to MCP clients.
const Version = "0.1.0"

// Server wires vaults and search into an MCP tool set.
type Server struct {
	vaults   map[string]*vault.Vault
	searcher *search.Searcher
}

// New returns a Server over the given vaults.
func New(vaults []*vault.Vault, searcher *search.Searcher) *Server {
	m := make(map[string]*vault.Vault, len(vaults))
	for _, v := range vaults {
		m[v.Name()] = v
	}
	return &Server{vaults: m, searcher: searcher}
}

// MCPServer builds the MCP server with all tools registered.
func (s *Server) MCPServer() *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "obsidian-hosted-mcp",
		Title:   "Obsidian Hosted MCP",
		Version: Version,
	}, nil)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_vaults",
		Description: "List the Obsidian vaults available on this server.",
	}, s.listVaults)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "list_notes",
		Description: "List notes and directories in a vault. Hidden folders such as .obsidian and .trash are excluded, " +
			"but passing dir \".trash\" lists deleted notes explicitly.",
	}, s.listNotes)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "read_note",
		Description: fmt.Sprintf("Read a note from a vault. Returns at most %d characters per call; "+
			"when the response is truncated, call again with offset set to next_offset to continue reading.", vault.ReadPageSize),
	}, s.readNote)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "search_notes",
		Description: "Full-text regex search across a vault (ripgrep syntax). Returns matching lines grouped by file, " +
			"optionally with surrounding context lines. Case-insensitive unless case_sensitive is set.",
	}, s.searchNotes)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "create_note",
		Description: "Create a new note. Parent directories are created automatically; fails if the note already exists.",
	}, s.createNote)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "append_note",
		Description: "Append content to a note, creating it if it does not exist.",
	}, s.appendNote)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "edit_note",
		Description: "Edit a note by replacing an exact text snippet. The snippet must occur exactly once " +
			"unless replace_all is set; include surrounding lines to make it unique.",
	}, s.editNote)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "move_note",
		Description: "Move or rename a note within a vault. Fails if the destination already exists.",
	}, s.moveNote)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "delete_note",
		Description: "Delete a note. By default it is moved to the vault's .trash folder (recoverable with restore_note); " +
			"set permanent to remove it outright.",
	}, s.deleteNote)

	mcp.AddTool(srv, &mcp.Tool{
		Name: "restore_note",
		Description: "Restore (undelete) a note from the vault's .trash folder. Restores to the note's path inside .trash " +
			"unless to is set; use list_notes with dir \".trash\" to see what can be restored.",
	}, s.restoreNote)

	return srv
}

// Handler returns the HTTP handler: a health endpoint at /healthz and the
// bearer-token-protected MCP endpoint everywhere else.
func (s *Server) Handler(token string) http.Handler {
	mcpHandler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return s.MCPServer()
	}, nil)
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})
	mux.Handle("/", requireBearer(token, mcpHandler))
	return mux
}

// requireBearer rejects requests whose Authorization header does not carry
// the expected bearer token.
func requireBearer(token string, next http.Handler) http.Handler {
	expected := "Bearer " + token
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("Authorization")
		if subtle.ConstantTimeCompare([]byte(got), []byte(expected)) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) vault(name string) (*vault.Vault, error) {
	v, ok := s.vaults[name]
	if !ok {
		return nil, fmt.Errorf("unknown vault %q: use list_vaults to see available vaults", name)
	}
	return v, nil
}

type listVaultsOutput struct {
	Vaults []string `json:"vaults" jsonschema:"names of the available vaults"`
}

func (s *Server) listVaults(context.Context, *mcp.CallToolRequest, any) (*mcp.CallToolResult, listVaultsOutput, error) {
	names := make([]string, 0, len(s.vaults))
	for name := range s.vaults {
		names = append(names, name)
	}
	sort.Strings(names)
	return nil, listVaultsOutput{Vaults: names}, nil
}

type listNotesInput struct {
	Vault     string `json:"vault" jsonschema:"name of the vault to list"`
	Dir       string `json:"dir,omitempty" jsonschema:"vault-relative directory to list; defaults to the vault root"`
	Recursive bool   `json:"recursive,omitempty" jsonschema:"list subdirectories recursively"`
}

type listNotesOutput struct {
	Entries []vault.Entry `json:"entries" jsonschema:"files and directories found"`
}

func (s *Server) listNotes(_ context.Context, _ *mcp.CallToolRequest, in listNotesInput) (*mcp.CallToolResult, listNotesOutput, error) {
	v, err := s.vault(in.Vault)
	if err != nil {
		return nil, listNotesOutput{}, err
	}
	entries, err := v.List(in.Dir, in.Recursive)
	if err != nil {
		return nil, listNotesOutput{}, err
	}
	return nil, listNotesOutput{Entries: entries}, nil
}

type readNoteInput struct {
	Vault  string `json:"vault" jsonschema:"name of the vault"`
	Path   string `json:"path" jsonschema:"vault-relative path of the note"`
	Offset int    `json:"offset,omitempty" jsonschema:"character offset to start reading from; use next_offset from a previous truncated read"`
}

func (s *Server) readNote(_ context.Context, _ *mcp.CallToolRequest, in readNoteInput) (*mcp.CallToolResult, *vault.ReadResult, error) {
	v, err := s.vault(in.Vault)
	if err != nil {
		return nil, nil, err
	}
	res, err := v.Read(in.Path, in.Offset)
	if err != nil {
		return nil, nil, err
	}
	return nil, res, nil
}

type searchNotesInput struct {
	Vault         string `json:"vault" jsonschema:"name of the vault to search"`
	Query         string `json:"query" jsonschema:"regular expression to search for (ripgrep syntax)"`
	Glob          string `json:"glob,omitempty" jsonschema:"restrict the search to paths matching this glob, e.g. *.md or daily/**"`
	CaseSensitive bool   `json:"case_sensitive,omitempty" jsonschema:"match case exactly instead of the default case-insensitive search"`
	ContextLines  int    `json:"context_lines,omitempty" jsonschema:"lines of context to include around each match"`
	MaxResults    int    `json:"max_results,omitempty" jsonschema:"maximum matching lines to return (default 50, max 500)"`
}

func (s *Server) searchNotes(ctx context.Context, _ *mcp.CallToolRequest, in searchNotesInput) (*mcp.CallToolResult, *search.Result, error) {
	v, err := s.vault(in.Vault)
	if err != nil {
		return nil, nil, err
	}
	res, err := s.searcher.Search(ctx, v.Root(), search.Options{
		Query:         in.Query,
		Glob:          in.Glob,
		CaseSensitive: in.CaseSensitive,
		ContextLines:  in.ContextLines,
		MaxResults:    in.MaxResults,
	})
	if err != nil {
		return nil, nil, err
	}
	return nil, res, nil
}

type writeNoteInput struct {
	Vault   string `json:"vault" jsonschema:"name of the vault"`
	Path    string `json:"path" jsonschema:"vault-relative path of the note"`
	Content string `json:"content" jsonschema:"markdown content"`
}

type okOutput struct {
	OK bool `json:"ok"`
}

func (s *Server) createNote(_ context.Context, _ *mcp.CallToolRequest, in writeNoteInput) (*mcp.CallToolResult, okOutput, error) {
	v, err := s.vault(in.Vault)
	if err != nil {
		return nil, okOutput{}, err
	}
	if err := v.Create(in.Path, in.Content); err != nil {
		return nil, okOutput{}, err
	}
	return nil, okOutput{OK: true}, nil
}

func (s *Server) appendNote(_ context.Context, _ *mcp.CallToolRequest, in writeNoteInput) (*mcp.CallToolResult, okOutput, error) {
	v, err := s.vault(in.Vault)
	if err != nil {
		return nil, okOutput{}, err
	}
	if err := v.Append(in.Path, in.Content); err != nil {
		return nil, okOutput{}, err
	}
	return nil, okOutput{OK: true}, nil
}

type editNoteInput struct {
	Vault      string `json:"vault" jsonschema:"name of the vault"`
	Path       string `json:"path" jsonschema:"vault-relative path of the note"`
	Find       string `json:"find" jsonschema:"exact text to replace; must occur exactly once unless replace_all is set"`
	Replace    string `json:"replace" jsonschema:"replacement text"`
	ReplaceAll bool   `json:"replace_all,omitempty" jsonschema:"replace every occurrence instead of requiring a unique match"`
}

type editNoteOutput struct {
	Replacements int `json:"replacements" jsonschema:"number of replacements made"`
}

func (s *Server) editNote(_ context.Context, _ *mcp.CallToolRequest, in editNoteInput) (*mcp.CallToolResult, editNoteOutput, error) {
	v, err := s.vault(in.Vault)
	if err != nil {
		return nil, editNoteOutput{}, err
	}
	n, err := v.Edit(in.Path, in.Find, in.Replace, in.ReplaceAll)
	if err != nil {
		return nil, editNoteOutput{}, err
	}
	return nil, editNoteOutput{Replacements: n}, nil
}

type moveNoteInput struct {
	Vault   string `json:"vault" jsonschema:"name of the vault"`
	Path    string `json:"path" jsonschema:"current vault-relative path of the note"`
	NewPath string `json:"new_path" jsonschema:"destination vault-relative path"`
}

func (s *Server) moveNote(_ context.Context, _ *mcp.CallToolRequest, in moveNoteInput) (*mcp.CallToolResult, okOutput, error) {
	v, err := s.vault(in.Vault)
	if err != nil {
		return nil, okOutput{}, err
	}
	if err := v.Move(in.Path, in.NewPath); err != nil {
		return nil, okOutput{}, err
	}
	return nil, okOutput{OK: true}, nil
}

type deleteNoteInput struct {
	Vault     string `json:"vault" jsonschema:"name of the vault"`
	Path      string `json:"path" jsonschema:"vault-relative path of the note"`
	Permanent bool   `json:"permanent,omitempty" jsonschema:"remove the note outright instead of moving it to .trash"`
}

type deleteNoteOutput struct {
	OK bool `json:"ok"`
	// TrashedTo is the vault-relative path the note was moved to inside
	// .trash; empty for permanent deletions.
	TrashedTo string `json:"trashed_to,omitempty" jsonschema:"where the note was moved inside .trash; empty when deleted permanently"`
}

type restoreNoteInput struct {
	Vault string `json:"vault" jsonschema:"name of the vault"`
	Path  string `json:"path" jsonschema:"vault-relative path of the note inside .trash"`
	To    string `json:"to,omitempty" jsonschema:"destination vault-relative path; defaults to the note's path inside .trash"`
}

type restoreNoteOutput struct {
	OK bool `json:"ok"`
	// RestoredTo is the vault-relative path the note was restored to.
	RestoredTo string `json:"restored_to" jsonschema:"vault-relative path the note was restored to"`
}

func (s *Server) restoreNote(_ context.Context, _ *mcp.CallToolRequest, in restoreNoteInput) (*mcp.CallToolResult, restoreNoteOutput, error) {
	v, err := s.vault(in.Vault)
	if err != nil {
		return nil, restoreNoteOutput{}, err
	}
	restoredTo, err := v.Restore(in.Path, in.To)
	if err != nil {
		return nil, restoreNoteOutput{}, err
	}
	return nil, restoreNoteOutput{OK: true, RestoredTo: restoredTo}, nil
}

func (s *Server) deleteNote(_ context.Context, _ *mcp.CallToolRequest, in deleteNoteInput) (*mcp.CallToolResult, deleteNoteOutput, error) {
	v, err := s.vault(in.Vault)
	if err != nil {
		return nil, deleteNoteOutput{}, err
	}
	trashedTo, err := v.Delete(in.Path, in.Permanent)
	if err != nil {
		return nil, deleteNoteOutput{}, err
	}
	return nil, deleteNoteOutput{OK: true, TrashedTo: trashedTo}, nil
}
