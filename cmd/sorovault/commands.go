package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

func loadContractIDs(args []string, path string, input io.Reader) ([]string, error) {
	ids := append([]string(nil), args...)
	if path == "" {
		return ids, nil
	}

	reader := input
	if path != "-" {
		// #nosec G304 -- the CLI operator explicitly selects this input file.
		file, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("opening contract ID file %s: %w", path, err)
		}
		defer func() { _ = file.Close() }()
		reader = file
	}

	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line, _, _ := strings.Cut(scanner.Text(), "#")
		if id := strings.TrimSpace(line); id != "" {
			ids = append(ids, id)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading contract ID file %s: %w", path, err)
	}
	return ids, nil
}

// writeJSON emits an indented JSON document followed by a newline.
//
// HTML escaping is off: this JSON is read by other programs, not embedded
// in a page, and escaping would turn a placeholder like <version> into
// \u003cversion\u003e, which is harder to read in a terminal and no safer
// once parsed.
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
