package vault

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestSectionBoundaries(t *testing.T) {
	cases := []struct {
		name, note string
		path       []string
		body       string
		level      int
	}{
		{"nested", "intro\n# Project\nlead\n## Tasks\none\n### Detail\ntwo\n## Done\nthree\n# Other\n", []string{"Project", "Tasks"}, "one\n### Detail\ntwo\n", 2},
		{"parent", "# Project\nlead\n## Tasks\none\n# Other\n", []string{"Project"}, "lead\n## Tasks\none\n", 1},
		{"setext", "Title\n=====\nintro\n\nTasks\n-----\nbody\n\nNext\n----\nend", []string{"Title", "Tasks"}, "body\n\n", 2},
		{"multiline setext", "A\nB\n===\nbody", []string{"A\nB"}, "body", 1},
		{"closing markers", "  ## Tasks ###\nbody\n## Done\n", []string{"Tasks"}, "body\n", 2},
		{"formatted title", "# **Tasks** `today`\nbody", []string{"**Tasks** `today`"}, "body", 1},
		{"empty heading", "#\nbody\n# Next\n", []string{""}, "body\n", 1},
		{"empty eof", "#", []string{""}, "", 1},
		{"no final newline", "# Tasks", []string{"Tasks"}, "", 1},
		{"crlf", "# Tasks\r\nbody\r\n# Next\r\n", []string{"Tasks"}, "body\r\n", 1},
		{"frontmatter", "---\n# Tasks\ntitle: test\n---\n# Tasks\nbody", []string{"Tasks"}, "body", 1},
		{"frontmatter dots", "---\n# Tasks\n...\n# Tasks\nbody", []string{"Tasks"}, "body", 1},
		{"fenced code", "# Tasks\n```md\n# Fake\n```\n~~~\n# Fake\n~~~\n# Next\n", []string{"Tasks"}, "```md\n# Fake\n```\n~~~\n# Fake\n~~~\n", 1},
		{"containers", "# Tasks\n\n> # Quote\n\n- # List\n\n    code\n\n# Next\n", []string{"Tasks"}, "\n> # Quote\n\n- # List\n\n    code\n\n", 1},
		{"html", "# Tasks\n<!--\n# Fake\n-->\n# Next\n", []string{"Tasks"}, "<!--\n# Fake\n-->\n", 1},
		{"skipped levels", "# Project\n### Tasks\nbody\n## Next\n", []string{"Project", "Tasks"}, "body\n", 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := newTestVault(t)
			mustWrite(t, v, "note.md", tc.note)
			got, err := v.GetSection("note.md", tc.path, 0)
			if err != nil {
				t.Fatal(err)
			}
			if got.Content != tc.body || got.Level != tc.level {
				t.Fatalf("got %+v page %+v; want %q level %d", got, got.ReadResult, tc.body, tc.level)
			}
		})
	}
}

func TestSectionSelectionErrors(t *testing.T) {
	v := newTestVault(t)
	mustWrite(t, v, "note.md", "# A\n## Tasks\na\n# B\n## Tasks\nb\n")
	for _, tc := range []struct {
		path     string
		headings []string
		offset   int
		want     string
	}{
		{"note.md", nil, 0, "at least one"},
		{"note.md", []string{"Missing"}, 0, "not found"},
		{"note.md", []string{"tasks"}, 0, "not found"},
		{"note.md", []string{"Tasks"}, 0, "ambiguous"},
		{"note.md", []string{"A", "Tasks"}, -1, "out of range"},
		{"note.md", []string{"A", "Tasks"}, 100, "out of range"},
		{"missing.md", []string{"A"}, 0, "reading"},
		{"../escape.md", []string{"A"}, 0, "escapes"},
	} {
		if _, err := v.GetSection(tc.path, tc.headings, tc.offset); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: %v", tc, err)
		}
	}
	got, err := v.GetSection("note.md", []string{"B", "Tasks"}, 0)
	if err != nil || got.Content != "b\n" || !slices.Equal(got.HeadingPath, []string{"B", "Tasks"}) {
		t.Fatalf("%+v %v", got, err)
	}
	for _, note := range []string{"plain text", "---\n# Unclosed frontmatter", "# A\n## Tasks\n## Tasks\n"} {
		mustWrite(t, v, "other.md", note)
		if err := v.ReplaceSection("other.md", []string{"Tasks"}, "oops"); err == nil {
			t.Fatal("unexpected replacement")
		}
		if got := mustReadFile(t, v, "other.md"); got != note {
			t.Fatal("failed replacement changed note")
		}
	}
}

func TestSectionPaging(t *testing.T) {
	v := newTestVault(t)
	body := strings.Repeat("✅", ReadPageSize) + "é"
	mustWrite(t, v, "note.md", "# Tasks\n"+body)
	first, err := v.GetSection("note.md", []string{"Tasks"}, 0)
	if err != nil || !first.Truncated || first.NextOffset != ReadPageSize || len([]rune(first.Content)) != ReadPageSize {
		t.Fatalf("%+v %v", first, err)
	}
	last, err := v.GetSection("note.md", []string{"Tasks"}, first.NextOffset)
	if err != nil || last.Content != "é" || last.Truncated || last.NextOffset != -1 || last.TotalCharacters != ReadPageSize+1 {
		t.Fatalf("%+v %v", last, err)
	}
	empty, err := v.GetSection("note.md", []string{"Tasks"}, ReadPageSize+1)
	if err != nil || empty.Content != "" {
		t.Fatalf("%+v %v", empty, err)
	}
}

func TestReplaceSection(t *testing.T) {
	for _, tc := range []struct{ name, note, content, want string }{
		{"subtree", "---\ntitle: Keep\n---\n# Project\nlead\n## Tasks\nold\n### Detail\nold\n## Done\nkeep\n", "new\n### New detail\nyes\n\n", "---\ntitle: Keep\n---\n# Project\nlead\n## Tasks\nnew\n### New detail\nyes\n\n## Done\nkeep\n"},
		{"separator", "## Tasks\nold\n## Done\nkeep", "new", "## Tasks\nnew\n## Done\nkeep"},
		{"empty", "## Tasks\nold\n## Done\nkeep", "", "## Tasks\n## Done\nkeep"},
		{"eof", "## Tasks\nold", "new", "## Tasks\nnew"},
		{"heading eof", "## Tasks", "new", "## Tasks\nnew"},
		{"empty eof", "## Tasks", "", "## Tasks"},
		{"setext", "Tasks\n-----\nold\n\nDone\n----\nkeep", "new", "Tasks\n-----\nnew\n\nDone\n----\nkeep"},
		{"setext hash title", "## Tasks\nold\n\n#literal\n----\nkeep", "new", "## Tasks\nnew\n\n#literal\n----\nkeep"},
		{"crlf", "## Tasks\r\nold\r\n## Done\r\nkeep", "new", "## Tasks\r\nnew\r\n## Done\r\nkeep"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := newTestVault(t)
			mustWrite(t, v, "note.md", tc.note)
			if err := v.ReplaceSection("note.md", []string{"Tasks"}, tc.content); err != nil {
				t.Fatal(err)
			}
			if got := mustReadFile(t, v, "note.md"); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestReplaceSectionWriteError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permissions do not restrict root")
	}
	v := newTestVault(t)
	mustWrite(t, v, "note.md", "# Tasks\nold")
	if err := os.Chmod(filepath.Join(v.Root(), "note.md"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := v.ReplaceSection("note.md", []string{"Tasks"}, "new"); err == nil || !strings.Contains(err.Error(), "writing") {
		t.Fatalf("%v", err)
	}
}
