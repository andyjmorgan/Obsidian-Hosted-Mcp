// Package tasks gives reminders stored as contextual Markdown checkboxes a
// first-class, agent-neutral API. The store is an ordinary note (by default
// Reminders.md) that stays human-editable in Obsidian; this package parses
// its task lines, addresses them by Obsidian block reference (^rem-xxxx),
// and rewrites only the block it changes so hand-written prose survives.
package tasks

import (
	"fmt"
	"regexp"
	"strings"
)

// DefaultFile is the vault-relative note that holds reminders when
// TASKS_FILE is not set.
const DefaultFile = "Reminders.md"

// idPrefix prefixes every generated block reference.
const idPrefix = "rem-"

// indentUnit is one nesting level when rendering; the parser accepts any
// indentation and infers nesting from relative width.
const indentUnit = "  "

// tabWidth is how many columns a tab counts for when comparing indentation.
const tabWidth = 4

// templateHeading names the section whose checkboxes document the format
// rather than record reminders; everything under it is ignored.
const templateHeading = "template"

// Task is one reminder: a checkbox line plus the contextual record beneath
// it. Nesting is reported through ParentID and Depth; see Store.Get for the
// flattened descendant list.
type Task struct {
	// ID is the Obsidian block reference (without the caret) that addresses
	// this task in every tool.
	ID string `json:"id" jsonschema:"stable identifier used to address the task"`
	// ParentID is set on subtasks.
	ParentID string `json:"parent_id,omitempty" jsonschema:"id of the parent task; empty for top-level reminders"`
	// Section is the innermost heading the task sits under.
	Section string `json:"section,omitempty" jsonschema:"heading the task is filed under"`
	Title   string `json:"title" jsonschema:"the action to take"`
	Done    bool   `json:"done" jsonschema:"whether the checkbox is ticked"`
	Due     string `json:"due,omitempty" jsonschema:"due date YYYY-MM-DD"`
	DueTime string `json:"due_time,omitempty" jsonschema:"due time HH:MM"`
	// Context explains why the reminder exists, so it is useful without the
	// conversation that created it.
	Context string   `json:"context,omitempty" jsonschema:"why this came up and why the follow-up matters"`
	Related []string `json:"related,omitempty" jsonschema:"wikilink targets of related notes"`
	Source  string   `json:"source,omitempty" jsonschema:"where the reminder came from"`
	// Closed is the closure handoff written when the task was completed.
	Closed string `json:"closed,omitempty" jsonschema:"how and when the task was resolved"`
	// Extra preserves bullets the format does not model (e.g. Depends on).
	Extra        []string `json:"extra,omitempty" jsonschema:"other bullets kept verbatim"`
	Depth        int      `json:"depth" jsonschema:"nesting depth; 0 for top-level reminders"`
	OpenSubtasks int      `json:"open_subtasks" jsonschema:"number of direct subtasks still open"`

	children []*Task
	// start and end delimit the task's lines, descendants included.
	start, end int
	// indent is the title line's leading whitespace, kept verbatim so a
	// rewritten block nests exactly where the original did.
	indent string
}

var (
	headingRe = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*\s*$`)
	taskRe    = regexp.MustCompile(`^(\s*)[-*+]\s+\[([ xX])\]\s+(.*)$`)
	fieldRe   = regexp.MustCompile(`^(\s*)[-*+]\s+\*\*([^*]+?):\*\*\s*(.*)$`)
	idRe      = regexp.MustCompile(`\s*\^([A-Za-z0-9-]+)\s*$`)
	dueRe     = regexp.MustCompile(`\s*📅\s*(\d{4}-\d{2}-\d{2})`)
	timeRe    = regexp.MustCompile(`\s*⏰\s*(\d{1,2}:\d{2})`)
	linkRe    = regexp.MustCompile(`\[\[([^\]]+)\]\]`)
	dueLineRe = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})(?:\s+(\d{1,2}:\d{2}))?$`)
)

// document is a parsed store file: its raw lines plus the task tree.
type document struct {
	lines    []string
	tasks    []*Task
	byID     map[string]*Task
	sections []section
}

// section records a heading's position and the extent of its body.
type section struct {
	line, level int
	title       string
}

// indentWidth returns the column width of a line's leading whitespace.
func indentWidth(line string) int {
	w := 0
	for _, r := range line {
		switch r {
		case ' ':
			w++
		case '\t':
			w += tabWidth
		default:
			return w
		}
	}
	return w
}

// stripIndent removes up to cols columns of leading whitespace.
func stripIndent(line string, cols int) string {
	w := 0
	for i, r := range line {
		if w >= cols || (r != ' ' && r != '\t') {
			return line[i:]
		}
		if r == ' ' {
			w++
		} else {
			w += tabWidth
		}
	}
	return ""
}

// parse builds the task tree from the store file's lines. Frontmatter,
// fenced code and the Template section are skipped; every other checkbox
// line is a task, nested by indentation, and the deeper-indented bullets
// that follow it are its fields.
func parse(lines []string) *document {
	doc := &document{lines: lines, byID: map[string]*Task{}}
	var (
		stack      []*Task
		headings   []section
		inFront    = len(lines) > 0 && strings.TrimSpace(lines[0]) == "---"
		inFence    bool
		inTemplate bool
		field      *string // field receiving continuation lines
		fieldCol   int
	)
	closeTo := func(n int) {
		stack = stack[:n]
		field = nil
	}
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case i == 0 && inFront:
			continue
		case inFront:
			if trimmed == "---" || trimmed == "..." {
				inFront = false
			}
			continue
		case strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~"):
			inFence = !inFence
			closeTo(0)
			continue
		case inFence:
			continue
		}
		if m := headingRe.FindStringSubmatch(line); m != nil {
			closeTo(0)
			level := len(m[1])
			for len(headings) > 0 && headings[len(headings)-1].level >= level {
				headings = headings[:len(headings)-1]
			}
			headings = append(headings, section{line: i, level: level, title: m[2]})
			doc.sections = append(doc.sections, headings[len(headings)-1])
			inTemplate = false
			for _, h := range headings {
				if strings.EqualFold(h.title, templateHeading) {
					inTemplate = true
				}
			}
			continue
		}
		if inTemplate {
			continue
		}
		if trimmed == "" {
			continue
		}
		indent := indentWidth(line)
		for len(stack) > 0 && indentWidth(stack[len(stack)-1].indent) >= indent {
			closeTo(len(stack) - 1)
		}
		if m := taskRe.FindStringSubmatch(line); m != nil {
			t := parseTitle(m[3])
			t.Done = m[2] != " "
			t.indent, t.start, t.Depth = m[1], i, len(stack)
			if len(headings) > 0 {
				t.Section = headings[len(headings)-1].title
			}
			if len(stack) > 0 {
				parent := stack[len(stack)-1]
				t.ParentID = parent.ID
				parent.children = append(parent.children, t)
			} else {
				doc.tasks = append(doc.tasks, t)
			}
			if t.ID != "" {
				doc.byID[t.ID] = t
			}
			stack = append(stack, t)
			field = nil
			for _, a := range stack {
				a.end = i + 1
			}
			continue
		}
		if len(stack) == 0 {
			continue
		}
		top := stack[len(stack)-1]
		for _, a := range stack {
			a.end = i + 1
		}
		if m := fieldRe.FindStringSubmatch(line); m != nil {
			field, fieldCol = top.setField(m[2], m[3], line)
			continue
		}
		if field != nil && indent > fieldCol {
			*field += "\n" + trimmed
			continue
		}
		field = nil
		top.Extra = append(top.Extra, stripIndent(line, indentWidth(top.indent)+len(indentUnit)))
	}
	for _, t := range doc.tasks {
		countOpen(t)
	}
	return doc
}

// countOpen fills OpenSubtasks down the tree.
func countOpen(t *Task) {
	for _, c := range t.children {
		if !c.Done {
			t.OpenSubtasks++
		}
		countOpen(c)
	}
}

// parseTitle splits a checkbox line's text into title, due markers and ID.
func parseTitle(rest string) *Task {
	t := &Task{}
	if m := idRe.FindStringSubmatchIndex(rest); m != nil {
		t.ID = rest[m[2]:m[3]]
		rest = rest[:m[0]]
	}
	if m := dueRe.FindStringSubmatchIndex(rest); m != nil {
		t.Due = rest[m[2]:m[3]]
		rest = rest[:m[0]] + rest[m[1]:]
	}
	if m := timeRe.FindStringSubmatchIndex(rest); m != nil {
		t.DueTime = rest[m[2]:m[3]]
		rest = rest[:m[0]] + rest[m[1]:]
	}
	rest = strings.TrimSpace(rest)
	if len(rest) > 4 && strings.HasPrefix(rest, "**") && strings.HasSuffix(rest, "**") {
		rest = rest[2 : len(rest)-2]
	}
	t.Title = rest
	return t
}

// setField stores a "- **Name:** value" bullet. Known names map onto the
// struct; anything else is preserved in Extra. It returns the field that
// continuation lines should extend, if any, and the bullet's column.
func (t *Task) setField(name, value, raw string) (*string, int) {
	col := indentWidth(raw)
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "context":
		t.Context = value
		return &t.Context, col
	case "source":
		t.Source = value
		return &t.Source, col
	case "closed":
		t.Closed = value
		return &t.Closed, col
	case "related":
		for _, m := range linkRe.FindAllStringSubmatch(value, -1) {
			t.Related = append(t.Related, m[1])
		}
		if len(t.Related) > 0 {
			return nil, col
		}
	case "due":
		if m := dueLineRe.FindStringSubmatch(strings.TrimSpace(value)); m != nil && t.Due == "" {
			t.Due, t.DueTime = m[1], m[2]
			return nil, col
		}
	}
	t.Extra = append(t.Extra, stripIndent(raw, indentWidth(t.indent)+len(indentUnit)))
	return nil, col
}

// render produces the task's lines, descendants included, with the title
// line indented by ind. Field order follows the store's documented template.
func (t *Task) render(ind string) []string {
	box := " "
	if t.Done {
		box = "x"
	}
	head := fmt.Sprintf("%s- [%s] **%s**", ind, box, t.Title)
	if t.Due != "" {
		head += " 📅 " + t.Due
	}
	if t.DueTime != "" {
		head += " ⏰ " + t.DueTime
	}
	if t.ID != "" {
		head += " ^" + t.ID
	}
	out := []string{head}
	fieldInd := ind + indentUnit
	out = append(out, renderField(fieldInd, "Context", t.Context)...)
	if len(t.Related) > 0 {
		links := make([]string, len(t.Related))
		for i, r := range t.Related {
			links[i] = "[[" + r + "]]"
		}
		out = append(out, fieldInd+"- **Related:** "+strings.Join(links, " · "))
	}
	out = append(out, renderField(fieldInd, "Source", t.Source)...)
	for _, e := range t.Extra {
		out = append(out, fieldInd+e)
	}
	out = append(out, renderField(fieldInd, "Closed", t.Closed)...)
	for _, c := range t.children {
		out = append(out, c.render(ind+indentUnit)...)
	}
	return out
}

// renderField emits a "- **Name:** value" bullet, with continuation lines
// indented one level further; nothing when value is empty.
func renderField(ind, name, value string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, "\n")
	out := []string{ind + "- **" + name + ":** " + parts[0]}
	for _, p := range parts[1:] {
		out = append(out, ind+indentUnit+p)
	}
	return out
}

// descendants returns the task's subtasks depth-first in file order.
func (t *Task) descendants() []*Task {
	var out []*Task
	for _, c := range t.children {
		out = append(out, c)
		out = append(out, c.descendants()...)
	}
	return out
}

// flatten returns every task in file order.
func (d *document) flatten() []*Task {
	var out []*Task
	for _, t := range d.tasks {
		out = append(out, t)
		out = append(out, t.descendants()...)
	}
	return out
}

// replaceBlock swaps a task's lines (descendants included) for repl.
func (d *document) replaceBlock(t *Task, repl []string) {
	d.lines = splice(d.lines, t.start, t.end, repl)
}

// removeBlock deletes a task's lines, collapsing a doubled blank line.
func (d *document) removeBlock(t *Task) {
	start, end := t.start, t.end
	if start > 0 && strings.TrimSpace(d.lines[start-1]) == "" &&
		(end >= len(d.lines) || strings.TrimSpace(d.lines[end]) == "") {
		start--
	}
	d.lines = splice(d.lines, start, end, nil)
}

// insertInSection appends a block at the end of the named section,
// creating the section at the end of the file when it does not exist.
func (d *document) insertInSection(title string, block []string) {
	for _, s := range d.sections {
		if !strings.EqualFold(s.title, title) {
			continue
		}
		end := len(d.lines)
		for _, n := range d.sections {
			if n.line > s.line && n.level <= s.level {
				end = n.line
				break
			}
		}
		at := end
		for at > s.line+1 && strings.TrimSpace(d.lines[at-1]) == "" {
			at--
		}
		repl := append([]string{""}, block...)
		repl = append(repl, "")
		d.lines = splice(d.lines, at, end, repl)
		return
	}
	for len(d.lines) > 0 && strings.TrimSpace(d.lines[len(d.lines)-1]) == "" {
		d.lines = d.lines[:len(d.lines)-1]
	}
	if len(d.lines) > 0 {
		d.lines = append(d.lines, "")
	}
	d.lines = append(d.lines, "## "+title, "")
	d.lines = append(d.lines, block...)
	d.lines = append(d.lines, "")
}

// splice replaces lines[start:end] with repl.
func splice(lines []string, start, end int, repl []string) []string {
	out := make([]string, 0, len(lines)-(end-start)+len(repl))
	out = append(out, lines[:start]...)
	out = append(out, repl...)
	return append(out, lines[end:]...)
}
