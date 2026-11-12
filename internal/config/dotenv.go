package config

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
)

// readDotenv parses the file at path. A missing file (or an empty path) is not
// an error and yields no variables.
func readDotenv(path string) (map[string]string, error) {
	if path == "" {
		return map[string]string{}, nil
	}
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return map[string]string{}, fmt.Errorf("read %s: %w", path, err)
	}
	defer f.Close()
	return parseDotenv(f)
}

// parseDotenv reads KEY=VALUE lines. Supported syntax:
//   - blank lines and lines starting with '#' are skipped
//   - an optional "export " prefix
//   - values in single or double quotes (no escape sequences); the quotes are removed
//   - unquoted values may end with an inline comment introduced by " #"
//
// Every malformed line is reported; the variables that did parse are returned too.
// Error messages carry line numbers only, never values.
func parseDotenv(r io.Reader) (map[string]string, error) {
	vals := map[string]string{}
	var errs []error

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if rest, ok := cutExportPrefix(line); ok {
			line = rest
		}

		key, value, ok := strings.Cut(line, "=")
		if !ok {
			errs = append(errs, fmt.Errorf("line %d: expected KEY=VALUE", lineNo))
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" || strings.ContainsAny(key, " \t") {
			errs = append(errs, fmt.Errorf("line %d: invalid variable name", lineNo))
			continue
		}
		v, err := parseDotenvValue(strings.TrimSpace(value))
		if err != nil {
			errs = append(errs, fmt.Errorf("line %d: %w", lineNo, err))
			continue
		}
		vals[key] = v
	}
	if err := sc.Err(); err != nil {
		errs = append(errs, fmt.Errorf("read: %w", err))
	}
	return vals, errors.Join(errs...)
}

// cutExportPrefix strips a leading "export" keyword followed by whitespace.
func cutExportPrefix(line string) (string, bool) {
	if !strings.HasPrefix(line, "export") || len(line) <= len("export") {
		return "", false
	}
	if c := line[len("export")]; c != ' ' && c != '\t' {
		return "", false
	}
	return strings.TrimLeft(line[len("export"):], " \t"), true
}

// parseDotenvValue interprets the right-hand side of a KEY=VALUE line.
func parseDotenvValue(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	if q := s[0]; q == '"' || q == '\'' {
		end := strings.IndexByte(s[1:], q)
		if end < 0 {
			return "", errors.New("unterminated quoted value")
		}
		rest := strings.TrimSpace(s[end+2:])
		if rest != "" && !strings.HasPrefix(rest, "#") {
			return "", errors.New("unexpected text after closing quote")
		}
		return s[1 : end+1], nil
	}
	for i := 1; i < len(s); i++ {
		if s[i] == '#' && (s[i-1] == ' ' || s[i-1] == '\t') {
			s = s[:i]
			break
		}
	}
	return strings.TrimSpace(s), nil
}
