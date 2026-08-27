// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2025-2026 lin-snow

package log

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeLogFile writes the given lines to a temp log file and returns its path.
func writeLogFile(t *testing.T, lines []string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.log")
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write log file: %v", err)
	}
	return path
}

func TestQueryLogFileTailMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.log")
	got, err := QueryLogFileTail(path, 0, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got == nil {
		t.Fatal("got nil slice, want empty non-nil slice")
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

func TestQueryLogFileTailSkipsBlankLines(t *testing.T) {
	path := writeLogFile(t, []string{
		`{"level":"info","msg":"alpha"}`,
		"",
		"   ",
		`{"level":"info","msg":"beta"}`,
	})
	got, err := QueryLogFileTail(path, 0, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"alpha", "beta"}
	if !equalStrings(msgsOf(got), want) {
		t.Errorf("msgs = %v, want %v", msgsOf(got), want)
	}
}

func TestQueryLogFileTailFilters(t *testing.T) {
	lines := []string{
		`{"level":"info","msg":"alpha"}`,
		`{"level":"error","msg":"beta error"}`,
		`{"level":"warn","msg":"gamma"}`,
		`{"level":"error","msg":"delta"}`,
		`not valid json`,
	}
	path := writeLogFile(t, lines)

	tests := []struct {
		name    string
		level   string
		keyword string
		want    []string
	}{
		{
			name: "no filter returns all non-empty",
			want: []string{"alpha", "beta error", "gamma", "delta", "not valid json"},
		},
		{
			name:  "level all returns all",
			level: "all",
			want:  []string{"alpha", "beta error", "gamma", "delta", "not valid json"},
		},
		{
			name:  "level error filter",
			level: "error",
			want:  []string{"beta error", "delta"},
		},
		{
			name:  "level filter is case-insensitive",
			level: "ERROR",
			want:  []string{"beta error", "delta"},
		},
		{
			name:    "keyword filter on msg",
			keyword: "delta",
			want:    []string{"delta"},
		},
		{
			name:    "level and keyword combined",
			level:   "error",
			keyword: "beta",
			want:    []string{"beta error"},
		},
		{
			name:    "keyword no match returns empty",
			keyword: "nonexistent",
			want:    []string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := QueryLogFileTail(path, 0, tt.level, tt.keyword)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !equalStrings(msgsOf(got), tt.want) {
				t.Errorf("msgs = %v, want %v", msgsOf(got), tt.want)
			}
		})
	}
}

func TestQueryLogFileTailDefaultLimit(t *testing.T) {
	// Write more than the default cap (200) to verify tail retention.
	const total = 205
	lines := make([]string, total)
	for i := range total {
		lines[i] = fmt.Sprintf(`{"level":"info","msg":"%d"}`, i)
	}
	path := writeLogFile(t, lines)

	got, err := QueryLogFileTail(path, 0, "", "") // limit<=0 -> 200
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 200 {
		t.Fatalf("len = %d, want 200 (default limit)", len(got))
	}
	// Tail retention: last 200 entries are lines 5..204.
	if got[0].Msg != "5" {
		t.Errorf("first kept Msg = %q, want %q", got[0].Msg, "5")
	}
	if got[len(got)-1].Msg != "204" {
		t.Errorf("last kept Msg = %q, want %q", got[len(got)-1].Msg, "204")
	}
}

func TestQueryLogFileTailMaxLimit(t *testing.T) {
	// Write more than the hard cap (5000) to verify clamping.
	const total = 5005
	lines := make([]string, total)
	for i := range total {
		lines[i] = `{"level":"info","msg":"x"}`
	}
	path := writeLogFile(t, lines)

	got, err := QueryLogFileTail(path, 10000, "", "") // limit>5000 -> 5000
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 5000 {
		t.Errorf("len = %d, want 5000 (clamped max)", len(got))
	}
}

func TestQueryLogFileTailExplicitLimitTail(t *testing.T) {
	lines := make([]string, 10)
	for i := range 10 {
		lines[i] = fmt.Sprintf(`{"level":"info","msg":"%d"}`, i)
	}
	path := writeLogFile(t, lines)

	got, err := QueryLogFileTail(path, 3, "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"7", "8", "9"}
	if !equalStrings(msgsOf(got), want) {
		t.Errorf("msgs = %v, want %v", msgsOf(got), want)
	}
}
