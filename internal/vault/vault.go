// Package vault provides sandboxed filesystem operations over a single
// synced Obsidian vault directory.
package vault

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// ReadPageSize is the maximum number of characters returned by a single
// Read call. Longer notes are paged via the offset parameter.
const ReadPageSize = 10240

// TrashDir is the vault-relative directory soft-deleted notes move to. It
// matches Obsidian's own "move to vault trash" convention, so deletions
// sync and remain recoverable from any device.
const TrashDir = ".trash"

// Vault exposes file operations rooted at a synced vault directory. All
// paths are vault-relative; attempts to escape the root are rejected.
type Vault struct {
	name string
	root string
}

// New returns a Vault named name rooted at the absolute directory root.
func New(name, root string) *Vault {
	return &Vault{name: name, root: root}
}

// Name returns the vault's name.
func (v *Vault) Name() string { return v.name }

// Root returns the vault's root directory.
func (v *Vault) Root() string { return v.root }

// Entry describes a file or directory inside the vault.
type Entry struct {
	// Path is the vault-relative path, using forward slashes.
	Path string `json:"path"`
	// IsDir reports whether the entry is a directory.
	IsDir bool `json:"is_dir"`
	// Size is the file size in bytes; zero for directories.
	Size int64 `json:"size"`
}

// ReadResult is one page of a note's content.
type ReadResult struct {
	// Content is up to ReadPageSize characters starting at Offset.
	Content string `json:"content"`
	// Offset is the character offset this page starts at.
	Offset int `json:"offset"`
	// TotalCharacters is the full length of the note in characters.
	TotalCharacters int `json:"total_characters"`
	// Truncated reports whether content remains after this page.
	Truncated bool `json:"truncated"`
	// NextOffset is the offset to pass to read the next page; -1 when the
	// note has been read to the end.
	NextOffset int `json:"next_offset"`
}

// resolve maps a vault-relative path to an absolute path, rejecting empty,
// absolute, and root-escaping paths.
func (v *Vault) resolve(rel string) (string, error) {
	if rel == "" {
		return "", errors.New("path must not be empty")
	}
	if path.IsAbs(rel) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("path %q must be vault-relative, not absolute", rel)
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the vault root", rel)
	}
	return filepath.Join(v.root, clean), nil
}

// List returns entries under dir (vault root when dir is empty), skipping
// hidden files and directories such as .obsidian and .trash. When recursive
// is true it descends into subdirectories.
func (v *Vault) List(dir string, recursive bool) ([]Entry, error) {
	base := v.root
	if dir != "" {
		abs, err := v.resolve(dir)
		if err != nil {
			return nil, err
		}
		base = abs
	}
	var entries []Entry
	if recursive {
		err := filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if p == base {
				return nil
			}
			if strings.HasPrefix(d.Name(), ".") {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			e, err := newEntry(v.root, p, d)
			if err != nil {
				return err
			}
			entries = append(entries, e)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("listing %q: %w", dir, err)
		}
	} else {
		dirents, err := os.ReadDir(base)
		if err != nil {
			return nil, fmt.Errorf("listing %q: %w", dir, err)
		}
		for _, d := range dirents {
			if strings.HasPrefix(d.Name(), ".") {
				continue
			}
			e, err := newEntry(v.root, filepath.Join(base, d.Name()), d)
			if err != nil {
				return nil, err
			}
			entries = append(entries, e)
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

func newEntry(root, abs string, d fs.DirEntry) (Entry, error) {
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return Entry{}, err
	}
	e := Entry{Path: filepath.ToSlash(rel), IsDir: d.IsDir()}
	if !d.IsDir() {
		info, err := d.Info()
		if err != nil {
			return Entry{}, err
		}
		e.Size = info.Size()
	}
	return e, nil
}

// Read returns one page of up to ReadPageSize characters of the note at
// path, starting at the character offset.
func (v *Vault) Read(rel string, offset int) (*ReadResult, error) {
	abs, err := v.resolve(rel)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, fmt.Errorf("reading %q: %w", rel, err)
	}
	runes := []rune(string(data))
	total := len(runes)
	if offset < 0 || offset > total {
		return nil, fmt.Errorf("offset %d is out of range: note has %d characters", offset, total)
	}
	end := min(offset+ReadPageSize, total)
	res := &ReadResult{
		Content:         string(runes[offset:end]),
		Offset:          offset,
		TotalCharacters: total,
		Truncated:       end < total,
		NextOffset:      -1,
	}
	if res.Truncated {
		res.NextOffset = end
	}
	return res, nil
}

// Create writes a new note at path, creating parent directories as needed.
// It fails if the note already exists.
func (v *Vault) Create(rel, content string) error {
	abs, err := v.resolve(rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return fmt.Errorf("creating parent directories for %q: %w", rel, err)
	}
	f, err := os.OpenFile(abs, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("note %q already exists: use append_note or edit_note to modify it", rel)
		}
		return fmt.Errorf("creating %q: %w", rel, err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		return fmt.Errorf("writing %q: %w", rel, err)
	}
	return f.Close()
}

// Append appends content to the note at path, creating it (and parent
// directories) if it does not exist.
func (v *Vault) Append(rel, content string) error {
	abs, err := v.resolve(rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return fmt.Errorf("creating parent directories for %q: %w", rel, err)
	}
	f, err := os.OpenFile(abs, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("opening %q for append: %w", rel, err)
	}
	defer f.Close()
	if _, err := f.WriteString(content); err != nil {
		return fmt.Errorf("appending to %q: %w", rel, err)
	}
	return f.Close()
}

// Edit replaces find with replace in the note at path and returns the number
// of replacements made. Unless replaceAll is true, find must occur exactly
// once.
func (v *Vault) Edit(rel, find, replace string, replaceAll bool) (int, error) {
	if find == "" {
		return 0, errors.New("find must not be empty")
	}
	abs, err := v.resolve(rel)
	if err != nil {
		return 0, err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return 0, fmt.Errorf("reading %q: %w", rel, err)
	}
	content := string(data)
	count := strings.Count(content, find)
	if count == 0 {
		return 0, fmt.Errorf("text not found in %q", rel)
	}
	if count > 1 && !replaceAll {
		return 0, fmt.Errorf("text occurs %d times in %q: provide more surrounding context to make it unique, or set replace_all", count, rel)
	}
	if err := os.WriteFile(abs, []byte(strings.ReplaceAll(content, find, replace)), 0o644); err != nil {
		return 0, fmt.Errorf("writing %q: %w", rel, err)
	}
	return count, nil
}

// Move renames a note from one vault-relative path to another, creating
// destination parent directories as needed. It fails if the destination
// already exists.
func (v *Vault) Move(from, to string) error {
	src, err := v.resolve(from)
	if err != nil {
		return err
	}
	dst, err := v.resolve(to)
	if err != nil {
		return err
	}
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("moving %q: %w", from, err)
	}
	if _, err := os.Stat(dst); err == nil {
		return fmt.Errorf("destination %q already exists", to)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("creating parent directories for %q: %w", to, err)
	}
	if err := os.Rename(src, dst); err != nil {
		return fmt.Errorf("moving %q to %q: %w", from, to, err)
	}
	return nil
}

// Delete removes the note at path. By default it is moved into the vault's
// .trash directory (recoverable, and the move syncs); when permanent is
// true the note is removed outright.
func (v *Vault) Delete(rel string, permanent bool) (trashedTo string, err error) {
	abs, err := v.resolve(rel)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(abs); err != nil {
		return "", fmt.Errorf("deleting %q: %w", rel, err)
	}
	if strings.HasPrefix(filepath.ToSlash(rel), TrashDir+"/") || rel == TrashDir {
		permanent = true
	}
	if permanent {
		if err := os.RemoveAll(abs); err != nil {
			return "", fmt.Errorf("deleting %q: %w", rel, err)
		}
		return "", nil
	}
	trashRoot := filepath.Join(v.root, TrashDir)
	if err := os.MkdirAll(trashRoot, 0o755); err != nil {
		return "", fmt.Errorf("creating trash directory: %w", err)
	}
	dst := uniquePath(filepath.Join(trashRoot, filepath.Base(abs)))
	if err := os.Rename(abs, dst); err != nil {
		return "", fmt.Errorf("moving %q to trash: %w", rel, err)
	}
	return TrashDir + "/" + filepath.Base(dst), nil
}

// uniquePath returns p, or p with " (n)" inserted before the extension when
// p already exists, matching how Obsidian resolves trash collisions.
func uniquePath(p string) string {
	if _, err := os.Stat(p); errors.Is(err, fs.ErrNotExist) {
		return p
	}
	ext := filepath.Ext(p)
	stem := strings.TrimSuffix(p, ext)
	for i := 1; ; i++ {
		candidate := fmt.Sprintf("%s (%d)%s", stem, i, ext)
		if _, err := os.Stat(candidate); errors.Is(err, fs.ErrNotExist) {
			return candidate
		}
	}
}
