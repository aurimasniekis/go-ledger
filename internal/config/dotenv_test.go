package config

import (
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestParseDotenv(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []EnvPair
	}{
		{"basic", "A=1\nB=two\n", []EnvPair{{"A", "1"}, {"B", "two"}}},
		{"blank and comments", "\n# c\n   # indented\nA=1\n\n", []EnvPair{{"A", "1"}}},
		{"export prefix", "export A=1\nexport\tB=2\n", []EnvPair{{"A", "1"}, {"B", "2"}}},
		{"export as a name", "exporter=1\nEXPORT=2\n", []EnvPair{{"exporter", "1"}, {"EXPORT", "2"}}},
		{"spaces around", "  A = 1  \n", []EnvPair{{"A", "1"}}},
		{"empty value", "A=\nB=''\nC=\"\"\n", []EnvPair{{"A", ""}, {"B", ""}, {"C", ""}}},
		{"double quoted", "A=\"hello world\"\n", []EnvPair{{"A", "hello world"}}},
		{"double quoted escapes", "A=\"l1\\nl2\\t\\\"q\\\" \\\\ \\r\"\n", []EnvPair{{"A", "l1\nl2\t\"q\" \\ \r"}}},
		{"double quoted keeps hash", "A=\"a # b\"\n", []EnvPair{{"A", "a # b"}}},
		{"single quoted literal", "A='no \\n escape $HOME'\n", []EnvPair{{"A", "no \\n escape $HOME"}}},
		{"no expansion", "A=$HOME\nB=\"${A}x\"\nC=$(id)\n", []EnvPair{{"A", "$HOME"}, {"B", "${A}x"}, {"C", "$(id)"}}},
		{"inline comment unquoted", "A=value # note\nB=x#y\n", []EnvPair{{"A", "value"}, {"B", "x#y"}}},
		{"comment after quote", "A=\"v\" # note\nB='w'#n\n", []EnvPair{{"A", "v"}, {"B", "w"}}},
		{"equals in value", "A=b=c\n", []EnvPair{{"A", "b=c"}}},
		{"crlf", "A=1\r\nB=\"2\"\r\n", []EnvPair{{"A", "1"}, {"B", "2"}}},
		{"bom", "\xef\xbb\xbfA=1\n", []EnvPair{{"A", "1"}}},
		{"last duplicate wins", "A=1\nB=2\nA=3\n", []EnvPair{{"A", "3"}, {"B", "2"}}},
		{"no trailing newline", "A=1", []EnvPair{{"A", "1"}}},
		{"empty file", "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseDotenv("f.env", []byte(tt.in))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseDotenvErrorsHaveFileAndLine(t *testing.T) {
	tests := []struct {
		name string
		in   string
		line int
		msg  string
	}{
		{"no equals", "A=1\nnot a pair\n", 2, "expected KEY=VALUE"},
		{"empty name", "A=1\n\n=x\n", 3, "invalid variable name"},
		{"digit first", "1A=x\n", 1, "invalid variable name"},
		{"dash in name", "A-B=x\n", 1, "invalid variable name"},
		{"space in name", "A B=x\n", 1, "invalid variable name"},
		{"export alone", "export\n", 1, "expected KEY=VALUE"},
		{"unterminated double", "# c\nA=\"abc\n", 2, "unterminated double-quoted"},
		{"unterminated single", "A='abc\n", 1, "unterminated single-quoted"},
		{"trailing backslash", "A=\"abc\\", 1, "unterminated double-quoted"},
		{"bad escape", "A=\"a\\qb\"\n", 1, "unsupported escape"},
		{"junk after quote", "A=\"a\" b\n", 1, "unexpected text after the closing quote"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseDotenv("dev.env", []byte(tt.in))
			var de *DotenvError
			if !errors.As(err, &de) {
				t.Fatalf("err = %v, want DotenvError", err)
			}
			if de.File != "dev.env" || de.Line != tt.line {
				t.Errorf("at %s:%d, want dev.env:%d", de.File, de.Line, tt.line)
			}
			if want := "dev.env:" + strconv.Itoa(tt.line) + ": " + tt.msg; !strings.HasPrefix(err.Error(), want) {
				t.Errorf("error = %q, want prefix %q", err.Error(), want)
			}
		})
	}
}

func TestDotenvErrorNeverEchoesValues(t *testing.T) {
	for _, in := range []string{"KEY=\"hunter2-secret\" trailing\n", "hunter2-secret\n", "KEY=\"hunter2-secret\\q\"\n"} {
		_, err := ParseDotenv("f", []byte(in))
		if err == nil || strings.Contains(err.Error(), "hunter2") {
			t.Fatalf("%q: err = %v", in, err)
		}
	}
}
