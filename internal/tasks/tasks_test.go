package tasks

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/andyjmorgan/obsidian-hosted-mcp/internal/vault"
)

// fixture mirrors the real Reminders.md: a documented preamble with a
// Template section that must be ignored, then an Inbox holding one
// hand-written reminder (no IDs yet) with two subtasks and an unmodelled
// "Depends on" bullet.
const fixture = `---
type: reminders
purpose: Agent-neutral contextual task and reminder store
---

# Reminders

Shared reminder store.

## How to add a reminder

### Template

- [ ] **Clear action to take** 📅 YYYY-MM-DD ⏰ HH:MM
  - **Context:** Why this came up.

## Inbox


- [ ] **Add a first-class task/reminder concept to the Obsidian MCP server**
  - **Context:** The current reminder system is stored in Obsidian as contextual Markdown records so it remains agent-neutral.
  - **Related:** [[Reminders]] · [[Personal/Notes/Kokoro]]
  - **Source:** ChatGPT voice conversation, 2026-10-04.
  - [ ] **Add first-class subtask support**
    - **Context:** Tasks need hierarchical work so a reminder can carry implementation steps.
  - [ ] **Add MCP-aware Obsidian task skills for filesystem agents**
    - **Context:** Once task operations exist, teach agents that Obsidian tasks are first-class state.
    - **Depends on:** first-class task/reminder MCP support and subtask support.
`

// counterRand yields predictable, distinct IDs: rem-0001, rem-0002, ...
type counterRand struct{ n uint16 }

func (c *counterRand) Read(p []byte) (int, error) {
	c.n++
	p[0], p[1] = byte(c.n>>8), byte(c.n)
	return 2, nil
}

type errRand struct{}

func (errRand) Read([]byte) (int, error) { return 0, errors.New("entropy exhausted") }

var fixedNow = func() time.Time { return time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC) }

func newStore(t *testing.T, content string) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "Reminders.md")
	if content != "" {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return New(vault.New("Me", dir), "Reminders.md", &counterRand{}, fixedNow), path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestListAssignsIDsOnceAndSkipsTemplate(t *testing.T) {
	s, path := newStore(t, fixture)
	tasks, err := s.List(StatusOpen, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 3 {
		t.Fatalf("got %d tasks, want 3 (template must be skipped): %+v", len(tasks), tasks)
	}
	root, sub1, sub2 := tasks[0], tasks[1], tasks[2]
	if root.ID != "rem-0001" || sub1.ID != "rem-0002" || sub2.ID != "rem-0003" {
		t.Errorf("ids = %s %s %s", root.ID, sub1.ID, sub2.ID)
	}
	if root.Title != "Add a first-class task/reminder concept to the Obsidian MCP server" || root.Section != "Inbox" || root.Depth != 0 || root.OpenSubtasks != 2 {
		t.Errorf("root = %+v", root)
	}
	if !strings.HasPrefix(root.Context, "The current reminder system") || root.Source != "ChatGPT voice conversation, 2026-10-04." {
		t.Errorf("root fields = %q / %q", root.Context, root.Source)
	}
	if !slices.Equal(root.Related, []string{"Reminders", "Personal/Notes/Kokoro"}) {
		t.Errorf("Related = %v", root.Related)
	}
	if sub1.ParentID != root.ID || sub1.Depth != 1 || sub2.ParentID != root.ID {
		t.Errorf("nesting: %+v / %+v", sub1, sub2)
	}
	if !slices.Equal(sub2.Extra, []string{"- **Depends on:** first-class task/reminder MCP support and subtask support."}) {
		t.Errorf("Extra = %q", sub2.Extra)
	}

	written := readFile(t, path)
	if !strings.Contains(written, "MCP server** ^rem-0001\n") || !strings.Contains(written, "subtask support** ^rem-0002\n") {
		t.Errorf("ids not written back:\n%s", written)
	}
	if strings.Contains(written, "Clear action to take** 📅 YYYY-MM-DD ⏰ HH:MM ^") {
		t.Error("template line was given an id")
	}
	// Only the title lines gained a suffix; everything else is byte-identical.
	stripped := strings.NewReplacer(" ^rem-0001", "", " ^rem-0002", "", " ^rem-0003", "").Replace(written)
	if stripped != fixture {
		t.Errorf("file changed beyond id suffixes:\n%s", written)
	}

	// A second read must not rewrite the file.
	before, _ := os.Stat(path)
	if _, err := s.List(StatusAll, ""); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(path)
	if readFile(t, path) != written || !after.ModTime().Equal(before.ModTime()) {
		t.Error("stable file was rewritten on a read")
	}
}

func TestListFilters(t *testing.T) {
	s, _ := newStore(t, "## Inbox\n\n- [ ] **open one** ^rem-aaaa\n- [x] **done one** ^rem-bbbb\n\n## Work\n\n- [ ] **work one** ^rem-cccc\n")
	cases := []struct {
		status  Status
		section string
		want    []string
	}{
		{StatusOpen, "", []string{"rem-aaaa", "rem-cccc"}},
		{"", "", []string{"rem-aaaa", "rem-cccc"}},
		{StatusDone, "", []string{"rem-bbbb"}},
		{StatusAll, "", []string{"rem-aaaa", "rem-bbbb", "rem-cccc"}},
		{StatusAll, "work", []string{"rem-cccc"}},
	}
	for _, c := range cases {
		got, err := s.List(c.status, c.section)
		if err != nil {
			t.Fatal(err)
		}
		ids := make([]string, len(got))
		for i, g := range got {
			ids[i] = g.ID
		}
		if !slices.Equal(ids, c.want) {
			t.Errorf("List(%q,%q) = %v, want %v", c.status, c.section, ids, c.want)
		}
	}
	if _, err := s.List("maybe", ""); err == nil {
		t.Error("invalid status accepted")
	}
}

func TestGetReturnsDescendants(t *testing.T) {
	s, _ := newStore(t, fixture)
	root, subs, err := s.Get("^rem-0001")
	if err != nil {
		t.Fatal(err)
	}
	if root.ID != "rem-0001" || len(subs) != 2 || subs[0].ID != "rem-0002" || subs[1].ID != "rem-0003" {
		t.Errorf("Get = %+v / %+v", root, subs)
	}
	if _, _, err := s.Get("rem-ffff"); err == nil || !strings.Contains(err.Error(), "list_tasks") {
		t.Errorf("unknown id error = %v", err)
	}
}

func TestCreateTopLevelAndValidation(t *testing.T) {
	s, path := newStore(t, fixture)
	bad := []CreateInput{
		{Title: "", Context: strings.Repeat("why ", 10)},
		{Title: "x", Context: "too short"},
		{Title: "x", Context: strings.Repeat("why ", 10), Due: "tomorrow"},
		{Title: "x", Context: strings.Repeat("why ", 10), DueTime: "9am"},
	}
	for _, in := range bad {
		if _, err := s.Create(in); err == nil {
			t.Errorf("Create(%+v) accepted", in)
		}
	}
	got, err := s.Create(CreateInput{
		Title:   "  Renew the domain  ",
		Context: "The registrar emailed a 30-day notice; auto-renew is off on purpose.",
		Related: []string{"[[Domains]]", " Billing "},
		Source:  "Registrar email 2026-10-01",
		Due:     "2026-10-20",
		DueTime: "09:00",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "rem-0004" || got.Section != "Inbox" || got.Title != "Renew the domain" || !slices.Equal(got.Related, []string{"Domains", "Billing"}) {
		t.Errorf("created = %+v", got)
	}
	want := "- [ ] **Renew the domain** 📅 2026-10-20 ⏰ 09:00 ^rem-0004\n" +
		"  - **Context:** The registrar emailed a 30-day notice; auto-renew is off on purpose.\n" +
		"  - **Related:** [[Domains]] · [[Billing]]\n" +
		"  - **Source:** Registrar email 2026-10-01\n"
	written := readFile(t, path)
	if !strings.HasSuffix(written, "- **Depends on:** first-class task/reminder MCP support and subtask support.\n\n"+want) {
		t.Errorf("block not appended to Inbox:\n%s", written)
	}
	tasks, err := s.List(StatusOpen, "")
	if err != nil || len(tasks) != 4 || tasks[3].ID != "rem-0004" || tasks[3].Due != "2026-10-20" || tasks[3].DueTime != "09:00" {
		t.Errorf("re-read = %+v, %v", tasks, err)
	}
}

func TestCreateInNewSectionBeforeNextHeading(t *testing.T) {
	s, path := newStore(t, "# Reminders\n\n## Inbox\n\n- [ ] **first** ^rem-aaaa\n\n## Later\n\ntext\n")
	ctx := "Enough context to pass the minimum length check easily."
	if _, err := s.Create(CreateInput{Title: "second", Context: ctx}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(CreateInput{Title: "third", Context: ctx, Section: "Someday"}); err != nil {
		t.Fatal(err)
	}
	want := "# Reminders\n\n## Inbox\n\n- [ ] **first** ^rem-aaaa\n\n- [ ] **second** ^rem-0001\n  - **Context:** " + ctx + "\n\n## Later\n\ntext\n\n## Someday\n\n- [ ] **third** ^rem-0002\n  - **Context:** " + ctx + "\n"
	if got := readFile(t, path); got != want {
		t.Errorf("file =\n%s\nwant\n%s", got, want)
	}
}

func TestCreateSubtaskAppendsUnderParent(t *testing.T) {
	s, path := newStore(t, fixture)
	ctx := "The parent needs an implementation step that can be tracked on its own."
	if _, err := s.Create(CreateInput{Title: "x", Context: ctx, ParentID: "rem-zzzz"}); err == nil {
		t.Error("unknown parent accepted")
	}
	got, err := s.Create(CreateInput{Title: "Write the parser", Context: ctx, ParentID: "rem-0001"})
	if err != nil {
		t.Fatal(err)
	}
	if got.ParentID != "rem-0001" || got.Depth != 1 || got.Section != "Inbox" {
		t.Errorf("subtask = %+v", got)
	}
	written := readFile(t, path)
	if !strings.Contains(written, "subtask support.\n  - [ ] **Write the parser** ^rem-0004\n    - **Context:** "+ctx+"\n") {
		t.Errorf("subtask not nested after last descendant:\n%s", written)
	}
	root, subs, err := s.Get("rem-0001")
	if err != nil || root.OpenSubtasks != 3 || len(subs) != 3 {
		t.Errorf("parent after add: %+v %d subs %v", root, len(subs), err)
	}
	// Nesting under a tab-indented parent keeps the child deeper than it.
	s2, path2 := newStore(t, "- [ ] **p** ^rem-aaaa\n\t- [ ] **c** ^rem-bbbb\n")
	if _, err := s2.Create(CreateInput{Title: "g", Context: ctx, ParentID: "rem-bbbb"}); err != nil {
		t.Fatal(err)
	}
	tasks, _ := s2.List(StatusAll, "")
	if len(tasks) != 3 || tasks[2].Depth != 2 || tasks[2].ParentID != "rem-bbbb" {
		t.Errorf("tab nesting broke: %+v\n%s", tasks, readFile(t, path2))
	}
}

func TestCompleteRequiresResolutionAndClosedSubtasks(t *testing.T) {
	s, path := newStore(t, fixture)
	if _, err := s.Complete("rem-0001", "", ""); err == nil {
		t.Error("empty resolution accepted")
	}
	if _, err := s.Complete("rem-0001", "done", "yesterday"); err == nil {
		t.Error("bad date accepted")
	}
	if _, err := s.Complete("rem-nope", "done", ""); err == nil {
		t.Error("unknown id accepted")
	}
	_, err := s.Complete("rem-0001", "Shipped it", "")
	if err == nil || !strings.Contains(err.Error(), "rem-0002, rem-0003") {
		t.Fatalf("parent with open subtasks: err = %v", err)
	}
	if _, err := s.Complete("rem-0002", "Implemented in tasks.go", "2026-10-05"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Complete("rem-0003", "Skill written", ""); err != nil {
		t.Fatal(err)
	}
	got, err := s.Complete("rem-0001", "Released in 0.8.0; see [[Release notes]]", "")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Done || got.Closed != "2026-10-04 — Released in 0.8.0; see [[Release notes]]" {
		t.Errorf("completed = %+v", got)
	}
	written := readFile(t, path)
	for _, want := range []string{
		"- [x] **Add a first-class task/reminder concept to the Obsidian MCP server** ^rem-0001\n",
		"  - **Source:** ChatGPT voice conversation, 2026-10-04.\n  - **Closed:** 2026-10-04 — Released in 0.8.0; see [[Release notes]]\n",
		"  - [x] **Add first-class subtask support** ^rem-0002\n    - **Context:** Tasks need hierarchical work so a reminder can carry implementation steps.\n    - **Closed:** 2026-10-05 — Implemented in tasks.go\n",
		"    - **Depends on:** first-class task/reminder MCP support and subtask support.\n    - **Closed:** 2026-10-04 — Skill written\n",
	} {
		if !strings.Contains(written, want) {
			t.Errorf("missing %q in:\n%s", want, written)
		}
	}
	open, _ := s.List(StatusOpen, "")
	if len(open) != 0 {
		t.Errorf("still open: %+v", open)
	}

	reopened, err := s.Reopen("rem-0001")
	if err != nil || reopened.Done || reopened.Closed == "" {
		t.Errorf("Reopen = %+v, %v", reopened, err)
	}
	if !strings.Contains(readFile(t, path), "- [ ] **Add a first-class task/reminder concept to the Obsidian MCP server** ^rem-0001\n") {
		t.Error("reopen did not untick")
	}
	if _, err := s.Reopen("rem-nope"); err == nil {
		t.Error("reopen unknown id accepted")
	}
}

func TestUpdate(t *testing.T) {
	s, path := newStore(t, fixture)
	if _, err := s.Update("rem-0001", UpdateInput{Due: "soon"}); err == nil {
		t.Error("bad due accepted")
	}
	if _, err := s.Update("rem-nope", UpdateInput{Title: "x"}); err == nil {
		t.Error("unknown id accepted")
	}
	got, err := s.Update("rem-0003", UpdateInput{
		Title: "Write the task skill", Context: "Teach agents to use the MCP task tools.", Source: "This session",
		Related: []string{"Skills"}, Due: "2026-11-01", DueTime: "10:30",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "Write the task skill" || got.Due != "2026-11-01" || got.DueTime != "10:30" || !slices.Equal(got.Related, []string{"Skills"}) {
		t.Errorf("updated = %+v", got)
	}
	want := "  - [ ] **Write the task skill** 📅 2026-11-01 ⏰ 10:30 ^rem-0003\n" +
		"    - **Context:** Teach agents to use the MCP task tools.\n" +
		"    - **Related:** [[Skills]]\n" +
		"    - **Source:** This session\n" +
		"    - **Depends on:** first-class task/reminder MCP support and subtask support.\n"
	if written := readFile(t, path); !strings.Contains(written, want) {
		t.Errorf("block =\n%s", written)
	}
	// Siblings and parent are untouched by a nested rewrite.
	if !strings.Contains(readFile(t, path), "  - [ ] **Add first-class subtask support** ^rem-0002\n    - **Context:** Tasks need") {
		t.Error("sibling disturbed")
	}
	got, err = s.Update("rem-0003", UpdateInput{ClearDue: true, Related: []string{}})
	if err != nil || got.Due != "" || got.DueTime != "" || len(got.Related) != 0 {
		t.Errorf("clear = %+v, %v", got, err)
	}
	if strings.Contains(readFile(t, path), "📅 2026-11-01") || strings.Contains(readFile(t, path), "[[Skills]]") {
		t.Error("due/related not cleared in file")
	}
}

func TestDelete(t *testing.T) {
	s, path := newStore(t, fixture)
	if err := s.Delete("rem-nope"); err == nil {
		t.Error("unknown id accepted")
	}
	if err := s.Delete("rem-0002"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(readFile(t, path), "subtask support**") {
		t.Error("subtask still present")
	}
	if err := s.Delete("rem-0001"); err != nil {
		t.Fatal(err)
	}
	written := readFile(t, path)
	if strings.Contains(written, "- [ ] **Add") || strings.Contains(written, "Depends on") {
		t.Errorf("tree not removed:\n%s", written)
	}
	if !strings.HasSuffix(written, "## Inbox\n\n") {
		t.Errorf("blank-line collapse wrong:\n%q", written[len(written)-30:])
	}
	tasks, _ := s.List(StatusAll, "")
	if len(tasks) != 0 {
		t.Errorf("tasks left: %+v", tasks)
	}
}

func TestMissingFileIsCreatedOnFirstWrite(t *testing.T) {
	s, path := newStore(t, "")
	tasks, err := s.List(StatusAll, "")
	if err != nil || len(tasks) != 0 {
		t.Fatalf("List on missing file = %v, %v", tasks, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Error("read created the file")
	}
	if _, err := s.Create(CreateInput{Title: "first", Context: "Created against an empty store to seed the header."}); err != nil {
		t.Fatal(err)
	}
	want := newFileHeader + "\n## Inbox\n\n- [ ] **first** ^rem-0001\n  - **Context:** Created against an empty store to seed the header.\n"
	if got := readFile(t, path); got != want {
		t.Errorf("new file =\n%s\nwant\n%s", got, want)
	}
}

func TestParseEdgeCases(t *testing.T) {
	src := strings.Join([]string{
		"---",
		"title: x",
		"...",
		"# Top #",
		"```",
		"- [ ] not a task, inside a fence",
		"```",
		"- [X] **Done caps** ^rem-aaaa",
		"\t- **Context:** first line",
		"\t  continued on a second line",
		"\t- **Related:** no links here",
		"\t- **Due:** 2026-12-01 08:15",
		"\t- plain bullet without a field",
		"\t  and its continuation",
		"",
		"- [ ] plain title without bold 📅 2026-01-02 ^rem-bbbb",
		"  - **Due:** 2026-03-04",
		"- [ ] **dup due** 📅 2026-05-06 ⏰ 7:30",
		"~~~",
		"- [ ] fenced tilde",
		"~~~",
		"",
	}, "\n")
	doc := parse(strings.Split(src, "\n"))
	if len(doc.tasks) != 3 {
		t.Fatalf("tasks = %d", len(doc.tasks))
	}
	a, b, c := doc.tasks[0], doc.tasks[1], doc.tasks[2]
	if !a.Done || a.Title != "Done caps" || a.Section != "Top" {
		t.Errorf("a = %+v", a)
	}
	if a.Context != "first line\ncontinued on a second line" {
		t.Errorf("continuation = %q", a.Context)
	}
	if a.Due != "2026-12-01" || a.DueTime != "08:15" {
		t.Errorf("due bullet = %q %q", a.Due, a.DueTime)
	}
	if !slices.Equal(a.Extra, []string{"- **Related:** no links here", "- plain bullet without a field", "  and its continuation"}) {
		t.Errorf("extra = %q", a.Extra)
	}
	if b.Title != "plain title without bold" || b.Due != "2026-01-02" || !slices.Equal(b.Extra, []string{"- **Due:** 2026-03-04"}) {
		t.Errorf("b = %+v", b)
	}
	if c.ID != "" || c.Due != "2026-05-06" || c.DueTime != "7:30" || c.Title != "dup due" {
		t.Errorf("c = %+v", c)
	}
	got := strings.Join(a.render(""), "\n")
	want := "- [x] **Done caps** 📅 2026-12-01 ⏰ 08:15 ^rem-aaaa\n" +
		"  - **Context:** first line\n    continued on a second line\n" +
		"  - **Related:** no links here\n  - plain bullet without a field\n    and its continuation"
	if got != want {
		t.Errorf("render =\n%s\nwant\n%s", got, want)
	}
}

func TestRoundTripCanonicalBlock(t *testing.T) {
	block := []string{
		"- [ ] **Parent** 📅 2026-10-10 ^rem-aaaa",
		"  - **Context:** why",
		"  - **Related:** [[A]] · [[B]]",
		"  - **Source:** here",
		"  - **Depends on:** nothing",
		"  - [x] **Child** ^rem-bbbb",
		"    - **Context:** because",
		"    - **Closed:** 2026-10-01 — done",
	}
	doc := parse(block)
	if got := doc.tasks[0].render(""); !slices.Equal(got, block) {
		t.Errorf("round trip =\n%s", strings.Join(got, "\n"))
	}
}

func TestIDGeneration(t *testing.T) {
	// Collisions are retried until an unused id appears.
	s := &Store{rand: bytes.NewReader([]byte{0, 1, 0, 1, 0, 2})}
	id, err := s.newID(map[string]*Task{"rem-0001": {}})
	if err != nil || id != "rem-0002" {
		t.Errorf("newID = %q, %v", id, err)
	}
	// Entropy failures surface from every path that mints an id.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Reminders.md"), []byte("- [ ] **no id**\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bad := New(vault.New("Me", dir), "Reminders.md", errRand{}, fixedNow)
	if _, err := bad.List(StatusAll, ""); err == nil || !strings.Contains(err.Error(), "entropy") {
		t.Errorf("List with failing rand: %v", err)
	}
	if _, err := bad.Create(CreateInput{Title: "x", Context: strings.Repeat("ctx ", 10)}); err == nil {
		t.Error("Create with failing rand succeeded")
	}
	_, _, err = bad.Get("rem-0001")
	if err == nil {
		t.Error("Get with failing rand succeeded")
	}
}

func TestIOErrors(t *testing.T) {
	dir := t.TempDir()
	// The store path is a directory: reads fail.
	if err := os.Mkdir(filepath.Join(dir, "Reminders.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := New(vault.New("Me", dir), "Reminders.md", &counterRand{}, fixedNow)
	for name, call := range map[string]func() error{
		"List": func() error { _, err := s.List(StatusAll, ""); return err },
		"Get":  func() error { _, _, err := s.Get("x"); return err },
		"Create": func() error {
			_, err := s.Create(CreateInput{Title: "x", Context: strings.Repeat("c ", 20)})
			return err
		},
		"Update":   func() error { _, err := s.Update("x", UpdateInput{}); return err },
		"Complete": func() error { _, err := s.Complete("x", "r", ""); return err },
		"Reopen":   func() error { _, err := s.Reopen("x"); return err },
		"Delete":   func() error { return s.Delete("x") },
	} {
		if err := call(); err == nil {
			t.Errorf("%s on unreadable store succeeded", name)
		}
	}
	// The parent of the store path is a file: writes fail.
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	w := New(vault.New("Me", blocker), "sub/Reminders.md", &counterRand{}, fixedNow)
	if _, err := w.Create(CreateInput{Title: "x", Context: strings.Repeat("c ", 20)}); err == nil {
		t.Error("Create under a file succeeded")
	}
	// A read that must persist ids fails when the file is not writable.
	ro := t.TempDir()
	p := filepath.Join(ro, "Reminders.md")
	if err := os.WriteFile(p, []byte("- [ ] **no id**\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	if os.Getuid() != 0 {
		r := New(vault.New("Me", ro), "Reminders.md", &counterRand{}, fixedNow)
		if _, err := r.List(StatusAll, ""); err == nil {
			t.Error("List on read-only store with missing ids succeeded")
		}
		if _, _, err := r.Get("rem-0001"); err == nil {
			t.Error("Get on read-only store with missing ids succeeded")
		}
	}
}

func TestHelpers(t *testing.T) {
	if indentWidth("\t  x") != tabWidth+2 || indentWidth("") != 0 {
		t.Error("indentWidth")
	}
	if stripIndent("      x", 2) != "    x" || stripIndent("  ", 4) != "" || stripIndent("x", 2) != "x" {
		t.Error("stripIndent")
	}
	if got := cleanLinks([]string{"", " [[A]] ", "B"}); !slices.Equal(got, []string{"A", "B"}) {
		t.Errorf("cleanLinks = %v", got)
	}
	var _ io.Reader = &counterRand{}
}
