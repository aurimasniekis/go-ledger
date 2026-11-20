package config

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
)

// EnvPair is one KEY=VALUE assignment read from a .env file.
type EnvPair struct{ Key, Value string }

// DotenvError is a malformed .env line. It carries the file name and line
// number and never the offending text, which may hold a secret.
type DotenvError struct {
	File string
	Line int
	Msg  string
}

func (e *DotenvError) Error() string { return fmt.Sprintf("%s:%d: %s", e.File, e.Line, e.Msg) }

// ParseDotenv parses .env text. The grammar is deliberately small:
//
//	# comment, blank lines
//	KEY=VALUE            unquoted: trimmed; " #" starts a comment
//	export KEY=VALUE     optional export prefix
//	KEY="a\nb"           double quotes: \n \r \t \" \\ escapes; no other escape
//	KEY='literal'        single quotes: taken literally
//
// There is no variable expansion and no multi-line value. Parsing stops at the
// first malformed line. A repeated key keeps its last value.
func ParseDotenv(name string, data []byte) ([]EnvPair, error) {
	text := strings.TrimPrefix(string(data), "\xef\xbb\xbf") // UTF-8 byte order mark
	var pairs []EnvPair
	index := map[string]int{}
	for i, line := range strings.Split(text, "\n") {
		n := i + 1
		fail := func(msg string) ([]EnvPair, error) { return nil, &DotenvError{File: name, Line: n, Msg: msg} }
		s := strings.TrimSpace(line)
		if s == "" || s[0] == '#' {
			continue
		}
		if rest, ok := strings.CutPrefix(s, "export"); ok && rest != "" && (rest[0] == ' ' || rest[0] == '\t') {
			s = strings.TrimSpace(rest)
		}
		key, val, ok := strings.Cut(s, "=")
		if !ok {
			return fail("expected KEY=VALUE")
		}
		key = strings.TrimSpace(key)
		if !validEnvKey(key) {
			return fail("invalid variable name (letters, digits and underscores; not starting with a digit)")
		}
		value, msg := parseDotenvValue(strings.TrimLeft(val, " \t"))
		if msg != "" {
			return fail(msg)
		}
		if at, dup := index[key]; dup {
			pairs[at].Value = value
			continue
		}
		index[key] = len(pairs)
		pairs = append(pairs, EnvPair{key, value})
	}
	return pairs, nil
}

func validEnvKey(k string) bool {
	if k == "" {
		return false
	}
	for i, r := range k {
		switch {
		case r == '_', r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

func parseDotenvValue(v string) (value, problem string) {
	if v == "" {
		return "", ""
	}
	switch v[0] {
	case '\'':
		end := strings.IndexByte(v[1:], '\'')
		if end < 0 {
			return "", "unterminated single-quoted value"
		}
		return v[1 : 1+end], trailing(v[end+2:])
	case '"':
		var b strings.Builder
		for i := 1; i < len(v); i++ {
			switch c := v[i]; c {
			case '"':
				return b.String(), trailing(v[i+1:])
			case '\\':
				i++
				if i >= len(v) {
					return "", "unterminated double-quoted value"
				}
				switch v[i] {
				case 'n':
					b.WriteByte('\n')
				case 'r':
					b.WriteByte('\r')
				case 't':
					b.WriteByte('\t')
				case '"', '\\':
					b.WriteByte(v[i])
				default:
					return "", fmt.Sprintf("unsupported escape \\%c in double-quoted value", v[i])
				}
			default:
				b.WriteByte(c)
			}
		}
		return "", "unterminated double-quoted value"
	}
	// Unquoted: a comment starts at whitespace followed by '#'.
	for i := 1; i < len(v); i++ {
		if v[i] == '#' && (v[i-1] == ' ' || v[i-1] == '\t') {
			v = v[:i]
			break
		}
	}
	return strings.TrimSpace(v), ""
}

// trailing accepts only whitespace or a comment after a closing quote.
func trailing(rest string) string {
	rest = strings.TrimSpace(rest)
	if rest == "" || rest[0] == '#' {
		return ""
	}
	return "unexpected text after the closing quote"
}

// loadDotenv reads and parses the file. A missing file is not an error.
func loadDotenv(readFile func(string) ([]byte, error), name string) (pairs []EnvPair, found bool, err error) {
	data, err := readFile(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read env file: %w", err)
	}
	pairs, err = ParseDotenv(name, data)
	return pairs, true, err
}
