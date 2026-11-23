package sysuser

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestParsePasswdBranches(t *testing.T) {
	t.Parallel()

	t.Run("open_error", func(t *testing.T) {
		t.Parallel()
		entries, err := parsePasswd("/nonexistent/passwd")
		if err == nil || entries != nil {
			t.Fatalf("expected open error, got entries=%v err=%v", entries, err)
		}
	})

	t.Run("valid_and_skipped_lines", func(t *testing.T) {
		t.Parallel()
		content := strings.Join([]string{
			"# header comment",
			"",
			"short:line",
			"daemon:x:1:1:daemon:/usr/sbin:/usr/sbin/nologin",
			"syslog:x:104:110::/home/syslog:/usr/sbin/nologin",
			"",
		}, "\n")
		p := filepath.Join(t.TempDir(), "passwd_clean")
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatalf("write file: %v", err)
		}
		entries, err := parsePasswd(p)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		expected := []passwdEntry{
			{name: "daemon", uid: 1, gid: 1},
			{name: "syslog", uid: 104, gid: 110},
		}
		if !slices.Equal(entries, expected) {
			t.Fatalf("expected %+v, got %+v", expected, entries)
		}
	})

	parseErrorCases := []struct {
		name    string
		content string
	}{
		{
			name:    "invalid_uid",
			content: "baduid:x:notanumber:100::/home:/bin/sh\n",
		},
		{
			name:    "invalid_gid",
			content: "badgid:x:100:notanumber::/home:/bin/sh\n",
		},
	}

	for _, tc := range parseErrorCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := filepath.Join(t.TempDir(), tc.name)
			if err := os.WriteFile(p, []byte(tc.content), 0o600); err != nil {
				t.Fatalf("write file: %v", err)
			}
			entries, err := parsePasswd(p)
			if err == nil || entries != nil {
				t.Fatalf("expected parse ids error, got entries=%v err=%v", entries, err)
			}
		})
	}

	t.Run("scanner_error", func(t *testing.T) {
		t.Parallel()
		p := filepath.Join(t.TempDir(), "passwd_huge")
		hugeLine := bytes.Repeat([]byte("x"), 70000)
		if err := os.WriteFile(p, hugeLine, 0o600); err != nil {
			t.Fatalf("write file: %v", err)
		}
		entries, err := parsePasswd(p)
		if err == nil || entries != nil || !strings.Contains(err.Error(), "scan passwd file") {
			t.Fatalf("expected scan error, got entries=%v err=%v", entries, err)
		}
	})
}

func TestParseGroupBranches(t *testing.T) {
	t.Parallel()

	t.Run("open_error", func(t *testing.T) {
		t.Parallel()
		entries, err := parseGroup("/nonexistent/group")
		if err == nil || entries != nil {
			t.Fatalf("expected open error, got entries=%v err=%v", entries, err)
		}
	})

	t.Run("valid_and_skipped_lines", func(t *testing.T) {
		t.Parallel()
		content := strings.Join([]string{
			"# group comment",
			"",
			"shortgroup",
			"adm:x:4:syslog,admin",
			"mail:x:8:",
			"",
		}, "\n")
		p := filepath.Join(t.TempDir(), "group_clean")
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatalf("write file: %v", err)
		}
		entries, err := parseGroup(p)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		expected := []groupEntry{
			{name: "adm", gid: 4},
			{name: "mail", gid: 8},
		}
		if !slices.Equal(entries, expected) {
			t.Fatalf("expected %+v, got %+v", expected, entries)
		}
	})

	t.Run("invalid_gid", func(t *testing.T) {
		t.Parallel()
		p := filepath.Join(t.TempDir(), "group_badgid")
		content := "badgrp:x:notanumber:\n"
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatalf("write file: %v", err)
		}
		entries, err := parseGroup(p)
		if err == nil || entries != nil || !strings.Contains(err.Error(), "parse group gid") {
			t.Fatalf("expected parse gid error, got entries=%v err=%v", entries, err)
		}
	})

	t.Run("scanner_error", func(t *testing.T) {
		t.Parallel()
		p := filepath.Join(t.TempDir(), "group_huge")
		hugeLine := bytes.Repeat([]byte("y"), 70000)
		if err := os.WriteFile(p, hugeLine, 0o600); err != nil {
			t.Fatalf("write file: %v", err)
		}
		entries, err := parseGroup(p)
		if err == nil || entries != nil || !strings.Contains(err.Error(), "scan group file") {
			t.Fatalf("expected scan error, got entries=%v err=%v", entries, err)
		}
	})
}

func TestWrapIfErr(t *testing.T) {
	t.Parallel()

	if err := wrapIfErr(nil, "act", "path"); err != nil {
		t.Fatalf("expected nil for nil err, got %v", err)
	}

	simulated := errors.New("simulated")
	wrapped := wrapIfErr(simulated, "check", "/etc/test")
	if wrapped == nil || !strings.Contains(wrapped.Error(), "check \"/etc/test\": simulated") {
		t.Fatalf("unexpected wrapped error: %v", wrapped)
	}
}

func TestFindHelpers(t *testing.T) {
	t.Parallel()

	users := []passwdEntry{
		{name: "alice", uid: 1001, gid: 1001},
		{name: "bob", uid: 1002, gid: 1002},
	}
	groups := []groupEntry{
		{name: "staff", gid: 1001},
		{name: "wheel", gid: 1002},
	}

	if u, ok := findUserByName(users, "alice"); !ok || u.uid != 1001 {
		t.Fatalf("expected to find alice, got ok=%v %+v", ok, u)
	}
	if _, ok := findUserByName(users, "nobody"); ok {
		t.Fatal("expected nobody not found")
	}

	if u, ok := findUserByUID(users, 1002); !ok || u.name != "bob" {
		t.Fatalf("expected to find bob by uid, got ok=%v %+v", ok, u)
	}
	if _, ok := findUserByUID(users, 9999); ok {
		t.Fatal("expected uid 9999 not found")
	}

	if g, ok := findGroupByName(groups, "staff"); !ok || g.gid != 1001 {
		t.Fatalf("expected to find staff, got ok=%v %+v", ok, g)
	}
	if _, ok := findGroupByName(groups, "nogroup"); ok {
		t.Fatal("expected nogroup not found")
	}

	if g, ok := findGroupByGID(groups, 1002); !ok || g.name != "wheel" {
		t.Fatalf("expected to find wheel by gid, got ok=%v %+v", ok, g)
	}
	if _, ok := findGroupByGID(groups, 9999); ok {
		t.Fatal("expected gid 9999 not found")
	}
}
