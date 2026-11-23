package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func mockLockAcquireReleaseErr(_ context.Context, _ string) (lockHandle, error) {
	return fakeLockHandle{releaseErr: errors.New("release failed")}, nil
}

func TestResolveArchiveArg(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		flagPath string
		want     string
		wantErr  string
		pos      []string
	}{
		{
			name:     "flag only",
			flagPath: "/backups/app-gamma.tar",
			want:     "/backups/app-gamma.tar",
		},
		{
			name: "positional only",
			pos:  []string{"/backups/app-delta.tar"},
			want: "/backups/app-delta.tar",
		},
		{
			name:     "rejects both flag and positional",
			flagPath: "/backups/a.tar",
			pos:      []string{"/backups/b.tar"},
			wantErr:  "cannot combine -f",
		},
		{
			name:    "rejects missing archive",
			wantErr: "requires exactly one",
		},
		{
			name:    "rejects multiple positionals",
			pos:     []string{"/backups/one.tar", "/backups/two.tar"},
			wantErr: "(or -f <archive.tar>)",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := resolveArchiveArg(tc.flagPath, tc.pos, "subcmd")
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("expected %q, got %q", tc.want, got)
			}
		})
	}
}

func TestSplitCSV(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{
			name: "empty string",
			raw:  "",
			want: nil,
		},
		{
			name: "whitespace only",
			raw:  "   \t\n  ",
			want: nil,
		},
		{
			name: "single element",
			raw:  "single",
			want: []string{"single"},
		},
		{
			name: "multiple elements with whitespace",
			raw:  " red , green , blue ",
			want: []string{"red", "green", "blue"},
		},
		{
			name: "empty parts ignored",
			raw:  "one, , two,, three",
			want: []string{"one", "two", "three"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := splitCSV(tc.raw)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("expected %v, got %v", tc.want, got)
			}
		})
	}
}

func TestWriteFormatted(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		writer io.Writer
		format string
		want   string
		arg    string
	}{
		{
			name:   "successful write",
			writer: &bytes.Buffer{},
			format: "hello %s",
			arg:    "world",
			want:   "hello world",
		},
		{
			name:   "writer error returns early",
			writer: failingWriter{},
			format: "hello %s",
			arg:    "world",
			want:   "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			writeFormatted(tc.writer, tc.format, tc.arg)
			if buf, ok := tc.writer.(*bytes.Buffer); ok {
				if got := buf.String(); got != tc.want {
					t.Fatalf("expected %q, got %q", tc.want, got)
				}
			}
		})
	}
}

func TestParseFlagsWithPositional(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		wantErr string
		args    []string
		wantPos []string
	}{
		{
			name:    "empty args",
			args:    nil,
			wantPos: nil,
		},
		{
			name:    "positionals only",
			args:    []string{"pos-a", "pos-b"},
			wantPos: []string{"pos-a", "pos-b"},
		},
		{
			name:    "flags and positionals interleaved",
			args:    []string{"--flag-a", "mid-a", "--flag-b", "mid-b"},
			wantPos: []string{"mid-a", "mid-b"},
		},
		{
			name:    "double dash separator stops flag parsing",
			args:    []string{"--flag-c", "--", "tail-a", "-not-a-flag"},
			wantPos: []string{"tail-a", "-not-a-flag"},
		},
		{
			name:    "unknown flag returns error",
			args:    []string{"--unknown-flag"},
			wantErr: "parse flags",
		},
		{
			name:    "flags only without positionals",
			args:    []string{"--flag-a", "--flag-b"},
			wantPos: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			fs := flag.NewFlagSet("test", flag.ContinueOnError)
			fs.SetOutput(io.Discard)
			_ = fs.Bool("flag-a", false, "")
			_ = fs.Bool("flag-b", false, "")
			_ = fs.Bool("flag-c", false, "")

			got, err := parseFlagsWithPositional(fs, tc.args)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !slices.Equal(got, tc.wantPos) {
				t.Fatalf("expected %v, got %v", tc.wantPos, got)
			}
		})
	}
}

func TestWithExclusiveLock(t *testing.T) {
	t.Run("default acquire executes fn and releases lock", func(t *testing.T) {
		lockDir := t.TempDir()
		lockPath := filepath.Join(lockDir, "test.lock")
		var errBuf bytes.Buffer
		executed := false

		err := withExclusiveLock(context.Background(), lockPath, &errBuf, func() error {
			executed = true
			return nil
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !executed {
			t.Fatalf("expected fn to be executed")
		}
		if errBuf.Len() > 0 {
			t.Fatalf("unexpected warning in stderr: %q", errBuf.String())
		}
	})

	t.Run("default acquire returns fn error", func(t *testing.T) {
		lockDir := t.TempDir()
		lockPath := filepath.Join(lockDir, "test.lock")
		var errBuf bytes.Buffer
		fnErr := errors.New("fn failed")

		err := withExclusiveLock(context.Background(), lockPath, &errBuf, func() error {
			return fnErr
		})
		if !errors.Is(err, fnErr) {
			t.Fatalf("expected %v, got %v", fnErr, err)
		}
	})

	t.Run("default acquire fails on canceled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		lockDir := t.TempDir()
		lockPath := filepath.Join(lockDir, "test.lock")
		var errBuf bytes.Buffer

		err := withExclusiveLock(ctx, lockPath, &errBuf, func() error {
			return nil
		})
		if err == nil || !strings.Contains(err.Error(), "acquire lock") {
			t.Fatalf("expected acquire lock error, got %v", err)
		}
	})

	t.Run("release error emits warning to errOut", func(t *testing.T) {
		orig := lockAcquire
		lockAcquire = mockLockAcquireReleaseErr
		t.Cleanup(func() { lockAcquire = orig })

		var errBuf bytes.Buffer
		err := withExclusiveLock(context.Background(), "mock.lock", &errBuf, func() error {
			return nil
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(errBuf.String(), "warning: release lock: release failed") {
			t.Fatalf("expected release lock warning, got %q", errBuf.String())
		}
	})
}

func TestPrintUsage(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	printUsage(&buf)
	if !strings.Contains(buf.String(), "Usage: bro <subcommand>") {
		t.Fatalf("expected usage text, got %q", buf.String())
	}
}
