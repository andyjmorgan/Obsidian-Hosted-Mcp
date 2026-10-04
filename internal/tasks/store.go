package tasks

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/andyjmorgan/obsidian-hosted-mcp/internal/vault"
)

// DefaultSection is where new top-level reminders are filed when no
// section is given.
const DefaultSection = "Inbox"

// minContext is the shortest context accepted on create: enough words to
// explain why the reminder exists, not a restatement of the title.
const minContext = 20

// newFileHeader seeds a store note that does not exist yet.
const newFileHeader = "---\ntype: reminders\nformat: contextual-markdown-tasks\n---\n\n# Reminders\n"

// Status filters List.
type Status string

const (
	// StatusOpen selects unticked tasks.
	StatusOpen Status = "open"
	// StatusDone selects ticked tasks.
	StatusDone Status = "done"
	// StatusAll selects every task.
	StatusAll Status = "all"
)

// Store reads and writes the reminders note of one vault.
type Store struct {
	path string
	rand io.Reader
	now  func() time.Time
}

// New returns a Store over the vault-relative note file inside v. rand
// supplies block-reference IDs and now stamps closures; both are injected
// for tests.
func New(v *vault.Vault, file string, rand io.Reader, now func() time.Time) *Store {
	return &Store{
		path: filepath.Join(v.Root(), filepath.FromSlash(file)),
		rand: rand,
		now:  now,
	}
}

// CreateInput describes a new reminder.
type CreateInput struct {
	Title   string
	Context string
	Related []string
	Source  string
	Due     string
	DueTime string
	// ParentID nests the new task under an existing one.
	ParentID string
	// Section files a top-level task under a heading; DefaultSection when empty.
	Section string
}

// UpdateInput changes fields of an existing reminder. Empty strings leave
// a field alone; nil Related leaves links alone and an empty slice clears
// them; ClearDue removes the due date and time.
type UpdateInput struct {
	Title    string
	Context  string
	Related  []string
	Source   string
	Due      string
	DueTime  string
	ClearDue bool
}

// load reads and parses the store, assigning IDs to tasks that lack one so
// hand-written reminders become addressable. New IDs are written back at
// once, so an ID seen in one call stays valid even if that call then fails.
// A missing file parses as an empty store and is only created on write.
func (s *Store) load() (*document, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		data = []byte(newFileHeader)
	} else if err != nil {
		return nil, fmt.Errorf("reading reminders: %w", err)
	}
	doc := parse(strings.Split(string(data), "\n"))
	changed := false
	for _, t := range doc.flatten() {
		if t.ID != "" {
			continue
		}
		id, err := s.newID(doc.byID)
		if err != nil {
			return nil, err
		}
		t.ID = id
		doc.byID[id] = t
		doc.lines[t.start] = strings.TrimRight(doc.lines[t.start], " \t") + " ^" + id
		for _, c := range t.children {
			c.ParentID = id
		}
		changed = true
	}
	if changed {
		if err := s.save(doc); err != nil {
			return nil, err
		}
	}
	return doc, nil
}

// save writes the document back, creating parent directories as needed.
func (s *Store) save(doc *document) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("creating reminders directory: %w", err)
	}
	if err := os.WriteFile(s.path, []byte(strings.Join(doc.lines, "\n")), 0o644); err != nil {
		return fmt.Errorf("writing reminders: %w", err)
	}
	return nil
}

// newID returns an unused block reference.
func (s *Store) newID(used map[string]*Task) (string, error) {
	for {
		buf := make([]byte, 2)
		if _, err := io.ReadFull(s.rand, buf); err != nil {
			return "", fmt.Errorf("generating task id: %w", err)
		}
		id := idPrefix + hex.EncodeToString(buf)
		if _, taken := used[id]; !taken {
			return id, nil
		}
	}
}

// List returns tasks matching status (and section, when non-empty) in file
// order, top-level and nested alike. It only writes the file when a task
// needed an ID.
func (s *Store) List(status Status, section string) ([]Task, error) {
	doc, err := s.load()
	if err != nil {
		return nil, err
	}
	out := []Task{}
	for _, t := range doc.flatten() {
		switch status {
		case StatusOpen, "":
			if t.Done {
				continue
			}
		case StatusDone:
			if !t.Done {
				continue
			}
		case StatusAll:
		default:
			return nil, fmt.Errorf("status must be open, done or all, got %q", status)
		}
		if section != "" && !strings.EqualFold(t.Section, section) {
			continue
		}
		out = append(out, *t)
	}
	return out, nil
}

// Get returns one task and its descendants, depth-first in file order.
func (s *Store) Get(id string) (*Task, []Task, error) {
	doc, err := s.load()
	if err != nil {
		return nil, nil, err
	}
	t, err := doc.find(id)
	if err != nil {
		return nil, nil, err
	}
	subs := []Task{}
	for _, d := range t.descendants() {
		subs = append(subs, *d)
	}
	return t, subs, nil
}

// find looks a task up by ID.
func (d *document) find(id string) (*Task, error) {
	t, ok := d.byID[strings.TrimPrefix(strings.TrimSpace(id), "^")]
	if !ok {
		return nil, fmt.Errorf("no task with id %q: use list_tasks to find ids", id)
	}
	return t, nil
}

// Create adds a reminder, nested under ParentID when set, otherwise at the
// end of Section. Context is mandatory: a reminder must make sense without
// the conversation that produced it.
func (s *Store) Create(in CreateInput) (*Task, error) {
	in.Title = strings.TrimSpace(in.Title)
	in.Context = strings.TrimSpace(in.Context)
	if in.Title == "" {
		return nil, errors.New("title is required: state the action to take")
	}
	if len(in.Context) < minContext {
		return nil, fmt.Errorf("context is required (at least %d characters): explain why this came up, what was decided and where the details live", minContext)
	}
	if err := validateDue(in.Due, in.DueTime); err != nil {
		return nil, err
	}
	doc, err := s.load()
	if err != nil {
		return nil, err
	}
	var parent *Task
	if in.ParentID != "" {
		if parent, err = doc.find(in.ParentID); err != nil {
			return nil, err
		}
	}
	id, err := s.newID(doc.byID)
	if err != nil {
		return nil, err
	}
	t := &Task{
		ID:      id,
		Title:   in.Title,
		Context: in.Context,
		Related: cleanLinks(in.Related),
		Source:  strings.TrimSpace(in.Source),
		Due:     in.Due,
		DueTime: in.DueTime,
	}
	if parent != nil {
		t.ParentID, t.Depth, t.Section = parent.ID, parent.Depth+1, parent.Section
		t.indent = parent.indent + indentUnit
		// Append after the parent's last descendant rather than re-rendering
		// the parent, so its hand-written lines are left untouched.
		doc.lines = splice(doc.lines, parent.end, parent.end, t.render(t.indent))
	} else {
		t.Section = in.Section
		if t.Section == "" {
			t.Section = DefaultSection
		}
		doc.insertInSection(t.Section, t.render(""))
	}
	if err := s.save(doc); err != nil {
		return nil, err
	}
	return t, nil
}

// Update changes the given fields of a task and rewrites its block.
func (s *Store) Update(id string, in UpdateInput) (*Task, error) {
	if err := validateDue(in.Due, in.DueTime); err != nil {
		return nil, err
	}
	doc, err := s.load()
	if err != nil {
		return nil, err
	}
	t, err := doc.find(id)
	if err != nil {
		return nil, err
	}
	if v := strings.TrimSpace(in.Title); v != "" {
		t.Title = v
	}
	if v := strings.TrimSpace(in.Context); v != "" {
		t.Context = v
	}
	if v := strings.TrimSpace(in.Source); v != "" {
		t.Source = v
	}
	if in.Related != nil {
		t.Related = cleanLinks(in.Related)
	}
	if in.ClearDue {
		t.Due, t.DueTime = "", ""
	}
	if in.Due != "" {
		t.Due = in.Due
	}
	if in.DueTime != "" {
		t.DueTime = in.DueTime
	}
	doc.replaceBlock(t, t.render(t.indent))
	if err := s.save(doc); err != nil {
		return nil, err
	}
	return t, nil
}

// Complete ticks a task and records the closure handoff. It refuses while
// direct subtasks remain open; date defaults to today.
func (s *Store) Complete(id, resolution, date string) (*Task, error) {
	resolution = strings.TrimSpace(resolution)
	if resolution == "" {
		return nil, errors.New("resolution is required: record how it was resolved, what changed and any follow-up")
	}
	if date == "" {
		date = s.now().Format("2006-01-02")
	} else if _, err := time.Parse("2006-01-02", date); err != nil {
		return nil, fmt.Errorf("date must be YYYY-MM-DD, got %q", date)
	}
	doc, err := s.load()
	if err != nil {
		return nil, err
	}
	t, err := doc.find(id)
	if err != nil {
		return nil, err
	}
	if t.OpenSubtasks > 0 {
		var open []string
		for _, c := range t.children {
			if !c.Done {
				open = append(open, c.ID)
			}
		}
		return nil, fmt.Errorf("task %s has open subtasks (%s): complete or delete them first", t.ID, strings.Join(open, ", "))
	}
	t.Done = true
	t.Closed = date + " — " + resolution
	doc.replaceBlock(t, t.render(t.indent))
	if err := s.save(doc); err != nil {
		return nil, err
	}
	return t, nil
}

// Reopen unticks a task, keeping its closure note for the record.
func (s *Store) Reopen(id string) (*Task, error) {
	doc, err := s.load()
	if err != nil {
		return nil, err
	}
	t, err := doc.find(id)
	if err != nil {
		return nil, err
	}
	t.Done = false
	doc.replaceBlock(t, t.render(t.indent))
	if err := s.save(doc); err != nil {
		return nil, err
	}
	return t, nil
}

// Delete removes a task and its subtasks from the note.
func (s *Store) Delete(id string) error {
	doc, err := s.load()
	if err != nil {
		return err
	}
	t, err := doc.find(id)
	if err != nil {
		return err
	}
	doc.removeBlock(t)
	return s.save(doc)
}

// validateDue checks the due date and time formats.
func validateDue(due, dueTime string) error {
	if due != "" {
		if _, err := time.Parse("2006-01-02", due); err != nil {
			return fmt.Errorf("due must be YYYY-MM-DD, got %q", due)
		}
	}
	if dueTime != "" {
		if _, err := time.Parse("15:04", dueTime); err != nil {
			return fmt.Errorf("due_time must be HH:MM (24-hour), got %q", dueTime)
		}
	}
	return nil
}

// cleanLinks normalises related-note entries to bare wikilink targets.
func cleanLinks(links []string) []string {
	var out []string
	for _, l := range links {
		l = strings.TrimSpace(l)
		l = strings.TrimSuffix(strings.TrimPrefix(l, "[["), "]]")
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}
