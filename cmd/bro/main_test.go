package main

import (
	"bytes"
	"io"
	"os"
	"testing"
)

func TestCLIEntrypoint(t *testing.T) {
	origArgs := os.Args
	origStdout := os.Stdout
	origStderr := os.Stderr
	origExit := osExit

	t.Cleanup(func() {
		os.Args = origArgs
		os.Stdout = origStdout
		os.Stderr = origStderr
		osExit = origExit
	})

	tests := []struct {
		name       string
		args       []string
		wantCode   int
		closePipe  bool
		wantCalled bool
	}{
		{
			name:       "success_exit",
			args:       []string{"--version"},
			wantCode:   0,
			closePipe:  false,
			wantCalled: false,
		},
		{
			name:       "error_exit_normal_stderr",
			args:       []string{"unknown-subcommand-alpha"},
			wantCode:   1,
			closePipe:  false,
			wantCalled: true,
		},
		{
			name:       "error_exit_failing_stderr",
			args:       []string{"unknown-subcommand-beta"},
			wantCode:   1,
			closePipe:  true,
			wantCalled: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var exitCalled bool
			var exitCode int

			osExit = func(code int) {
				exitCalled = true
				exitCode = code
			}

			os.Args = append([]string{"bro"}, tt.args...)

			outR, outW, err := os.Pipe()
			if err != nil {
				t.Fatalf("create stdout pipe: %v", err)
			}
			os.Stdout = outW

			errR, errW, err := os.Pipe()
			if err != nil {
				t.Fatalf("create stderr pipe: %v", err)
			}

			if tt.closePipe {
				if cErr := errR.Close(); cErr != nil {
					t.Fatalf("close errR: %v", cErr)
				}
				if cErr := errW.Close(); cErr != nil {
					t.Fatalf("close errW: %v", cErr)
				}
				os.Stderr = errW
			} else {
				os.Stderr = errW
			}

			main()

			if !tt.closePipe {
				if cErr := errW.Close(); cErr != nil {
					t.Fatalf("close errW: %v", cErr)
				}
				var errBuf bytes.Buffer
				if _, cErr := io.Copy(&errBuf, errR); cErr != nil {
					t.Fatalf("copy stderr: %v", cErr)
				}
				if cErr := errR.Close(); cErr != nil {
					t.Fatalf("close errR: %v", cErr)
				}
			}

			if cErr := outW.Close(); cErr != nil {
				t.Fatalf("close outW: %v", cErr)
			}
			if cErr := outR.Close(); cErr != nil {
				t.Fatalf("close outR: %v", cErr)
			}

			if exitCalled != tt.wantCalled {
				t.Fatalf("exit called mismatch: got %v, want %v", exitCalled, tt.wantCalled)
			}
			if exitCode != tt.wantCode {
				t.Fatalf("exit code mismatch: got %d, want %d", exitCode, tt.wantCode)
			}
		})
	}
}
