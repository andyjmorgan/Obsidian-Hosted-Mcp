package vault

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestVault(t *testing.T) *Vault {
	t.Helper()
	return New("Test", t.TempDir())
}

func mustWrite(t *testing.T, v *Vault, rel, content string) {
	t.Helper()
	abs := filepath.Join(v.Root(), filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustReadFile(t *testing.T, v *Vault, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(v.Root(), filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestNameAndRoot(t *testing.T) {
	v := New("Notes", "/data/Notes")
	if v.Name() != "Notes" || v.Root() != "/data/Notes" {
		t.Errorf("got %q %q", v.Name(), v.Root())
	}
}

func TestResolveRejectsBadPaths(t *testing.T) {
	v := newTestVault(t)
	for _, rel := range []string{"", "/etc/passwd", "..", "../outside", "a/../../b", "./.."} {
		t.Run(rel, func(t *testing.T) {
			if _, err := v.resolve(rel); err == nil {
				t.Errorf("resolve(%q) succeeded", rel)
			}
		})
	}
}

func TestResolveAllowsInternalPaths(t *testing.T) {
	v := newTestVault(t)
	for _, rel := range []string{"note.md", "dir/note.md", "a/../b.md", "./note.md"} {
		if _, err := v.resolve(rel); err != nil {
			t.Errorf("resolve(%q): %v", rel, err)
		}
	}
}

func TestList(t *testing.T) {
	v := newTestVault(t)
	mustWrite(t, v, "b.md", "bb")
	mustWrite(t, v, "a.md", "a")
	mustWrite(t, v, "daily/2026-07-03.md", "today")
	mustWrite(t, v, ".obsidian/app.json", "{}")
	mustWrite(t, v, ".trash/old.md", "gone")

	t.Run("root non-recursive", func(t *testing.T) {
		entries, err := v.List("", false)
		if err != nil {
			t.Fatal(err)
		}
		want := []Entry{
			{Path: "a.md", Size: 1},
			{Path: "b.md", Size: 2},
			{Path: "daily", IsDir: true},
		}
		assertEntries(t, entries, want)
	})

	t.Run("recursive", func(t *testing.T) {
		entries, err := v.List("", true)
		if err != nil {
			t.Fatal(err)
		}
		want := []Entry{
			{Path: "a.md", Size: 1},
			{Path: "b.md", Size: 2},
			{Path: "daily", IsDir: true},
			{Path: "daily/2026-07-03.md", Size: 5},
		}
		assertEntries(t, entries, want)
	})

	t.Run("subdirectory", func(t *testing.T) {
		entries, err := v.List("daily", false)
		if err != nil {
			t.Fatal(err)
		}
		assertEntries(t, entries, []Entry{{Path: "daily/2026-07-03.md", Size: 5}})
	})

	t.Run("missing dir", func(t *testing.T) {
		if _, err := v.List("nope", false); err == nil {
			t.Error("List succeeded on missing dir")
		}
		if _, err := v.List("nope", true); err == nil {
			t.Error("recursive List succeeded on missing dir")
		}
	})

	t.Run("bad path", func(t *testing.T) {
		if _, err := v.List("../elsewhere", false); err == nil {
			t.Error("List escaped the root")
		}
	})
}

func assertEntries(t *testing.T, got, want []Entry) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestReadSmallNote(t *testing.T) {
	v := newTestVault(t)
	mustWrite(t, v, "note.md", "hello world")
	res, err := v.Read("note.md", 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "hello world" || res.TotalCharacters != 11 || res.Truncated || res.NextOffset != -1 {
		t.Errorf("got %+v", res)
	}
}

func TestReadPaging(t *testing.T) {
	v := newTestVault(t)
	content := strings.Repeat("x", ReadPageSize) + strings.Repeat("y", 100)
	mustWrite(t, v, "big.md", content)

	first, err := v.Read("big.md", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Content) != ReadPageSize || !first.Truncated || first.NextOffset != ReadPageSize {
		t.Fatalf("first page: len=%d truncated=%v next=%d", len(first.Content), first.Truncated, first.NextOffset)
	}
	second, err := v.Read("big.md", first.NextOffset)
	if err != nil {
		t.Fatal(err)
	}
	if second.Content != strings.Repeat("y", 100) || second.Truncated || second.NextOffset != -1 {
		t.Fatalf("second page: %+v", second)
	}
	if second.TotalCharacters != ReadPageSize+100 {
		t.Errorf("TotalCharacters = %d", second.TotalCharacters)
	}
}

func TestReadCountsCharactersNotBytes(t *testing.T) {
	v := newTestVault(t)
	mustWrite(t, v, "unicode.md", "héllo — ünïcode ✅")
	res, err := v.Read("unicode.md", 0)
	if err != nil {
		t.Fatal(err)
	}
	want := len([]rune("héllo — ünïcode ✅"))
	if res.TotalCharacters != want {
		t.Errorf("TotalCharacters = %d, want %d", res.TotalCharacters, want)
	}
	if res.Content != "héllo — ünïcode ✅" {
		t.Errorf("Content = %q", res.Content)
	}
}

func TestReadErrors(t *testing.T) {
	v := newTestVault(t)
	mustWrite(t, v, "note.md", "abc")
	if _, err := v.Read("missing.md", 0); err == nil {
		t.Error("Read succeeded on missing note")
	}
	if _, err := v.Read("note.md", -1); err == nil {
		t.Error("Read accepted negative offset")
	}
	if _, err := v.Read("note.md", 4); err == nil {
		t.Error("Read accepted offset beyond end")
	}
	if _, err := v.Read("../note.md", 0); err == nil {
		t.Error("Read escaped the root")
	}
}

func TestCreate(t *testing.T) {
	v := newTestVault(t)
	if err := v.Create("new/nested/note.md", "content"); err != nil {
		t.Fatal(err)
	}
	if got := mustReadFile(t, v, "new/nested/note.md"); got != "content" {
		t.Errorf("content = %q", got)
	}
	if err := v.Create("new/nested/note.md", "again"); err == nil {
		t.Error("Create overwrote an existing note")
	}
	if err := v.Create("../escape.md", "x"); err == nil {
		t.Error("Create escaped the root")
	}
	if err := v.Create("new/nested/note.md/child.md", "x"); err == nil {
		t.Error("Create treated a file as a directory")
	}
}

func TestAppend(t *testing.T) {
	v := newTestVault(t)
	if err := v.Append("log.md", "one\n"); err != nil {
		t.Fatal(err)
	}
	if err := v.Append("log.md", "two\n"); err != nil {
		t.Fatal(err)
	}
	if got := mustReadFile(t, v, "log.md"); got != "one\ntwo\n" {
		t.Errorf("content = %q", got)
	}
	if err := v.Append("deep/dir/log.md", "x"); err != nil {
		t.Errorf("Append with new parents: %v", err)
	}
	if err := v.Append("../escape.md", "x"); err == nil {
		t.Error("Append escaped the root")
	}
	if err := v.Append("deep/dir/log.md/child.md", "x"); err == nil {
		t.Error("Append treated a file as a directory")
	}
}

func TestEdit(t *testing.T) {
	v := newTestVault(t)
	mustWrite(t, v, "note.md", "alpha beta alpha")

	if _, err := v.Edit("note.md", "", "x", false); err == nil {
		t.Error("Edit accepted empty find")
	}
	if _, err := v.Edit("note.md", "gamma", "x", false); err == nil {
		t.Error("Edit succeeded on missing text")
	}
	if _, err := v.Edit("note.md", "alpha", "x", false); err == nil {
		t.Error("Edit replaced ambiguous text without replace_all")
	}
	n, err := v.Edit("note.md", "beta", "B", false)
	if err != nil || n != 1 {
		t.Fatalf("Edit unique: n=%d err=%v", n, err)
	}
	n, err = v.Edit("note.md", "alpha", "A", true)
	if err != nil || n != 2 {
		t.Fatalf("Edit replace_all: n=%d err=%v", n, err)
	}
	if got := mustReadFile(t, v, "note.md"); got != "A B A" {
		t.Errorf("content = %q", got)
	}
	if _, err := v.Edit("missing.md", "a", "b", false); err == nil {
		t.Error("Edit succeeded on missing note")
	}
	if _, err := v.Edit("../x.md", "a", "b", false); err == nil {
		t.Error("Edit escaped the root")
	}
}

func TestMove(t *testing.T) {
	v := newTestVault(t)
	mustWrite(t, v, "old.md", "content")
	if err := v.Move("old.md", "archive/new.md"); err != nil {
		t.Fatal(err)
	}
	if got := mustReadFile(t, v, "archive/new.md"); got != "content" {
		t.Errorf("content = %q", got)
	}
	if _, err := os.Stat(filepath.Join(v.Root(), "old.md")); !os.IsNotExist(err) {
		t.Error("source still exists")
	}

	mustWrite(t, v, "a.md", "a")
	mustWrite(t, v, "b.md", "b")
	if err := v.Move("a.md", "b.md"); err == nil {
		t.Error("Move overwrote existing destination")
	}
	if err := v.Move("missing.md", "x.md"); err == nil {
		t.Error("Move succeeded on missing source")
	}
	if err := v.Move("../x.md", "y.md"); err == nil {
		t.Error("Move escaped the root (source)")
	}
	if err := v.Move("a.md", "../y.md"); err == nil {
		t.Error("Move escaped the root (destination)")
	}
}

func TestDeleteSoft(t *testing.T) {
	v := newTestVault(t)
	mustWrite(t, v, "dir/note.md", "content")
	trashedTo, err := v.Delete("dir/note.md", false)
	if err != nil {
		t.Fatal(err)
	}
	if trashedTo != ".trash/note.md" {
		t.Errorf("trashedTo = %q", trashedTo)
	}
	if got := mustReadFile(t, v, ".trash/note.md"); got != "content" {
		t.Errorf("trash content = %q", got)
	}
}

func TestDeleteSoftNameCollision(t *testing.T) {
	v := newTestVault(t)
	mustWrite(t, v, "a/note.md", "first")
	mustWrite(t, v, "b/note.md", "second")
	if _, err := v.Delete("a/note.md", false); err != nil {
		t.Fatal(err)
	}
	trashedTo, err := v.Delete("b/note.md", false)
	if err != nil {
		t.Fatal(err)
	}
	if trashedTo != ".trash/note (1).md" {
		t.Errorf("trashedTo = %q", trashedTo)
	}
	if got := mustReadFile(t, v, ".trash/note (1).md"); got != "second" {
		t.Errorf("trash content = %q", got)
	}
}

func TestDeletePermanent(t *testing.T) {
	v := newTestVault(t)
	mustWrite(t, v, "note.md", "content")
	trashedTo, err := v.Delete("note.md", true)
	if err != nil {
		t.Fatal(err)
	}
	if trashedTo != "" {
		t.Errorf("trashedTo = %q, want empty", trashedTo)
	}
	if _, err := os.Stat(filepath.Join(v.Root(), "note.md")); !os.IsNotExist(err) {
		t.Error("note still exists")
	}
}

func TestDeleteInsideTrashIsPermanent(t *testing.T) {
	v := newTestVault(t)
	mustWrite(t, v, ".trash/old.md", "x")
	if _, err := v.Delete(".trash/old.md", false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(v.Root(), ".trash", "old.md")); !os.IsNotExist(err) {
		t.Error("trash entry still exists")
	}
	entries, err := os.ReadDir(filepath.Join(v.Root(), ".trash"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("trash not empty: %v", entries)
	}
}

func TestDeleteErrors(t *testing.T) {
	v := newTestVault(t)
	if _, err := v.Delete("missing.md", false); err == nil {
		t.Error("Delete succeeded on missing note")
	}
	if _, err := v.Delete("../x.md", false); err == nil {
		t.Error("Delete escaped the root")
	}
}

func TestListSkipsHiddenFilesRecursively(t *testing.T) {
	v := newTestVault(t)
	mustWrite(t, v, ".DS_Store", "junk")
	mustWrite(t, v, "note.md", "content")
	entries, err := v.List("", true)
	if err != nil {
		t.Fatal(err)
	}
	assertEntries(t, entries, []Entry{{Path: "note.md", Size: 7}})
}

type fakeDirEntry struct {
	name    string
	dir     bool
	infoErr error
}

func (f fakeDirEntry) Name() string               { return f.name }
func (f fakeDirEntry) IsDir() bool                { return f.dir }
func (f fakeDirEntry) Type() fs.FileMode          { return 0 }
func (f fakeDirEntry) Info() (fs.FileInfo, error) { return nil, f.infoErr }

func TestNewEntryErrors(t *testing.T) {
	if _, err := newEntry("relative-root", "/abs/file.md", fakeDirEntry{name: "file.md", dir: true}); err == nil {
		t.Error("newEntry succeeded with unrelatable paths")
	}
	if _, err := newEntry("/root", "/root/file.md", fakeDirEntry{name: "file.md", infoErr: errors.New("stat failed")}); err == nil {
		t.Error("newEntry succeeded despite Info failure")
	}
}

func TestCreateNameTooLong(t *testing.T) {
	v := newTestVault(t)
	if err := v.Create(strings.Repeat("x", 300)+".md", "content"); err == nil {
		t.Error("Create accepted an over-long file name")
	}
}

func TestAppendToDirectory(t *testing.T) {
	v := newTestVault(t)
	mustWrite(t, v, "dir/inner.md", "x")
	if err := v.Append("dir", "content"); err == nil {
		t.Error("Append succeeded on a directory")
	}
}

func TestMoveDestinationParentIsFile(t *testing.T) {
	v := newTestVault(t)
	mustWrite(t, v, "src.md", "x")
	mustWrite(t, v, "blocker.md", "y")
	if err := v.Move("src.md", "blocker.md/nested.md"); err == nil {
		t.Error("Move succeeded through a file")
	}
}

func TestMoveDirectoryIntoItself(t *testing.T) {
	v := newTestVault(t)
	mustWrite(t, v, "d/inner.md", "x")
	if err := v.Move("d", "d/sub"); err == nil {
		t.Error("Move succeeded moving a directory into itself")
	}
}

func TestDeleteSoftTrashBlockedByFile(t *testing.T) {
	v := newTestVault(t)
	mustWrite(t, v, "note.md", "x")
	if err := os.WriteFile(filepath.Join(v.Root(), TrashDir), []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Delete("note.md", false); err == nil {
		t.Error("Delete succeeded with .trash blocked by a file")
	}
}

func TestListTrashExplicitly(t *testing.T) {
	v := newTestVault(t)
	mustWrite(t, v, ".trash/old.md", "gone")
	mustWrite(t, v, ".trash/sub/nested.md", "deep")
	entries, err := v.List(TrashDir, true)
	if err != nil {
		t.Fatal(err)
	}
	want := []Entry{
		{Path: ".trash/old.md", Size: 4},
		{Path: ".trash/sub", IsDir: true},
		{Path: ".trash/sub/nested.md", Size: 4},
	}
	assertEntries(t, entries, want)
}

func TestListEmptyDirMarshalsAsArray(t *testing.T) {
	v := newTestVault(t)
	entries, err := v.List("", false)
	if err != nil {
		t.Fatal(err)
	}
	if entries == nil {
		t.Fatal("List returned a nil slice; it must marshal as [], not null")
	}
}

func TestRestoreDefaultDestination(t *testing.T) {
	v := newTestVault(t)
	mustWrite(t, v, "dir/note.md", "content")
	if _, err := v.Delete("dir/note.md", false); err != nil {
		t.Fatal(err)
	}
	restoredTo, err := v.Restore(".trash/note.md", "")
	if err != nil {
		t.Fatal(err)
	}
	if restoredTo != "note.md" {
		t.Errorf("restoredTo = %q, want %q", restoredTo, "note.md")
	}
	if got := mustReadFile(t, v, "note.md"); got != "content" {
		t.Errorf("restored content = %q", got)
	}
	if _, err := os.Stat(filepath.Join(v.Root(), ".trash/note.md")); !os.IsNotExist(err) {
		t.Error("note still present in .trash after restore")
	}
}

func TestRestoreExplicitDestination(t *testing.T) {
	v := newTestVault(t)
	mustWrite(t, v, ".trash/note.md", "content")
	restoredTo, err := v.Restore(".trash/note.md", "dir/back.md")
	if err != nil {
		t.Fatal(err)
	}
	if restoredTo != "dir/back.md" {
		t.Errorf("restoredTo = %q", restoredTo)
	}
	if got := mustReadFile(t, v, "dir/back.md"); got != "content" {
		t.Errorf("restored content = %q", got)
	}
}

func TestRestoreErrors(t *testing.T) {
	v := newTestVault(t)
	mustWrite(t, v, "live.md", "x")
	mustWrite(t, v, ".trash/live.md", "y")
	mustWrite(t, v, ".trash/ghost-dest.md", "z")

	if _, err := v.Restore("live.md", ""); err == nil {
		t.Error("Restore accepted a path outside .trash")
	}
	if _, err := v.Restore(".trash/missing.md", ""); err == nil {
		t.Error("Restore accepted a missing trash note")
	}
	if _, err := v.Restore(".trash/live.md", ""); err == nil {
		t.Error("Restore clobbered an existing destination")
	}
	if _, err := v.Restore(".trash/ghost-dest.md", "../escape.md"); err == nil {
		t.Error("Restore accepted an escaping destination")
	}
	if _, err := v.Restore(TrashDir, ""); err == nil {
		t.Error("Restore accepted .trash itself")
	}
}
