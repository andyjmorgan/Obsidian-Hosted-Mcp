// Package search runs full-text searches over a vault directory using
// ripgrep, parsing its JSON event stream into structured matches.
package search

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// DefaultMaxResults caps matches returned when the caller does not specify
// a limit.
const DefaultMaxResults = 50

// MaxResultsCeiling is the hard upper bound on matches per search.
const MaxResultsCeiling = 500

// Options configures a single search.
type Options struct {
	// Query is the regular expression to search for (ripgrep syntax).
	Query string
	// Glob optionally restricts the search to matching paths,
	// e.g. "*.md" or "daily/**".
	Glob string
	// CaseSensitive disables the default case-insensitive matching.
	CaseSensitive bool
	// ContextLines is the number of lines of context to include around
	// each match.
	ContextLines int
	// MaxResults caps the number of matching lines returned; zero means
	// DefaultMaxResults.
	MaxResults int
}

// Line is a single line of search output.
type Line struct {
	// Number is the 1-based line number within the file.
	Number int `json:"line"`
	// Text is the line's content, without a trailing newline.
	Text string `json:"text"`
	// Match reports whether the line matched the query (false for
	// context lines).
	Match bool `json:"match"`
}

// FileMatches groups the matching lines of one file.
type FileMatches struct {
	// Path is the vault-relative path of the file.
	Path string `json:"path"`
	// Lines are the matching and context lines, in file order.
	Lines []Line `json:"lines"`
}

// Result is the outcome of a search.
type Result struct {
	// Files lists each file with matches, in path order.
	Files []FileMatches `json:"files"`
	// TotalMatches counts matching lines across all files.
	TotalMatches int `json:"total_matches"`
	// Truncated reports whether MaxResults cut the result off.
	Truncated bool `json:"truncated"`
}

// RunFunc executes a command in dir and returns its stdout, stderr, and
// exit code. It only returns an error when the command could not be run at
// all.
type RunFunc func(ctx context.Context, dir, name string, args ...string) (stdout, stderr []byte, exitCode int, err error)

// Searcher runs ripgrep searches rooted at vault directories.
type Searcher struct {
	binary string
	run    RunFunc
}

// New returns a Searcher that executes ripgrep as binary (typically "rg")
// via run. Passing nil for run uses os/exec.
func New(binary string, run RunFunc) *Searcher {
	if run == nil {
		run = execRun
	}
	return &Searcher{binary: binary, run: run}
}

func execRun(ctx context.Context, dir, name string, args ...string) ([]byte, []byte, int, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return stdout.Bytes(), stderr.Bytes(), exitErr.ExitCode(), nil
	}
	if err != nil {
		return nil, nil, 0, err
	}
	return stdout.Bytes(), stderr.Bytes(), 0, nil
}

// Search runs the query over the vault rooted at root. Hidden directories
// (.obsidian, .trash, ...) are excluded because ripgrep skips hidden files
// by default; --no-ignore prevents any stray ignore files in the vault from
// silently hiding notes.
func (s *Searcher) Search(ctx context.Context, root string, opts Options) (*Result, error) {
	if strings.TrimSpace(opts.Query) == "" {
		return nil, errors.New("query must not be empty")
	}
	maxResults := opts.MaxResults
	if maxResults <= 0 {
		maxResults = DefaultMaxResults
	}
	maxResults = min(maxResults, MaxResultsCeiling)

	args := []string{"--json", "--no-ignore", "--sort", "path"}
	if !opts.CaseSensitive {
		args = append(args, "--ignore-case")
	}
	if opts.ContextLines > 0 {
		args = append(args, "--context", fmt.Sprint(opts.ContextLines))
	}
	if opts.Glob != "" {
		args = append(args, "--glob", opts.Glob)
	}
	args = append(args, "--regexp", opts.Query, "--", ".")

	stdout, stderr, exitCode, err := s.run(ctx, root, s.binary, args...)
	if err != nil {
		return nil, fmt.Errorf("running %s: %w", s.binary, err)
	}
	// ripgrep exits 0 on matches, 1 on no matches, 2 on error.
	if exitCode > 1 {
		return nil, fmt.Errorf("search failed: %s", strings.TrimSpace(string(stderr)))
	}
	return parseJSONEvents(stdout, maxResults)
}

// event is the subset of ripgrep's --json output the parser consumes.
type event struct {
	Type string `json:"type"`
	Data struct {
		Path struct {
			Text string `json:"text"`
		} `json:"path"`
		Lines struct {
			Text string `json:"text"`
		} `json:"lines"`
		LineNumber int `json:"line_number"`
	} `json:"data"`
}

func parseJSONEvents(out []byte, maxResults int) (*Result, error) {
	res := &Result{}
	var current *FileMatches
	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var ev event
		if err := json.Unmarshal(line, &ev); err != nil {
			return nil, fmt.Errorf("parsing search output: %w", err)
		}
		if ev.Type != "match" && ev.Type != "context" {
			continue
		}
		if ev.Type == "match" && res.TotalMatches >= maxResults {
			res.Truncated = true
			break
		}
		path := strings.TrimPrefix(ev.Data.Path.Text, "./")
		if current == nil || current.Path != path {
			res.Files = append(res.Files, FileMatches{Path: path})
			current = &res.Files[len(res.Files)-1]
		}
		current.Lines = append(current.Lines, Line{
			Number: ev.Data.LineNumber,
			Text:   strings.TrimRight(ev.Data.Lines.Text, "\n"),
			Match:  ev.Type == "match",
		})
		if ev.Type == "match" {
			res.TotalMatches++
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scanning search output: %w", err)
	}
	return res, nil
}
