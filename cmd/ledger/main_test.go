package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestUnknownCommandMessageHasNoBlankLine(t *testing.T) {
	var out, errOut bytes.Buffer
	run([]string{"bogus"}, &out, &errOut)
	lines := strings.Split(errOut.String(), "\n")
	if lines[0] != `ledger: unknown command "bogus"` {
		t.Fatalf("first stderr line = %q", lines[0])
	}
	if lines[1] == "" {
		t.Errorf("blank line after the unknown-command error: %q", errOut.String())
	}
}

func TestRun(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantOut  string
		wantErr  string
	}{
		{name: "version", args: []string{"version"}, wantCode: 0, wantOut: "ledger dev\n"},
		{name: "version flag", args: []string{"--version"}, wantCode: 0, wantOut: "ledger dev\n"},
		{name: "help", args: []string{"help"}, wantCode: 0, wantOut: "usage: ledger <command>"},
		{name: "no arguments prints usage to stderr", args: nil, wantCode: 2, wantErr: "usage: ledger <command>"},
		{name: "unknown command", args: []string{"bogus"}, wantCode: 2, wantErr: `unknown command "bogus"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			code := run(tt.args, &out, &errOut)
			if code != tt.wantCode {
				t.Fatalf("exit code = %d, want %d (stderr %q)", code, tt.wantCode, errOut.String())
			}
			if tt.wantOut != "" && !strings.Contains(out.String(), tt.wantOut) {
				t.Errorf("stdout = %q, want it to contain %q", out.String(), tt.wantOut)
			}
			if tt.wantErr != "" && !strings.Contains(errOut.String(), tt.wantErr) {
				t.Errorf("stderr = %q, want it to contain %q", errOut.String(), tt.wantErr)
			}
		})
	}
}
