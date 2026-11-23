package sysuser

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type passwdEntry struct {
	name string
	uid  uint32
	gid  uint32
}

type groupEntry struct {
	name string
	gid  uint32
}

func parsePasswd(path string) ([]passwdEntry, error) {
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("open passwd file %q: %w", path, err)
	}

	var entries []passwdEntry
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) < 4 {
			continue
		}
		uid, uidErr := strconv.ParseUint(fields[2], 10, 32)
		gid, gidErr := strconv.ParseUint(fields[3], 10, 32)
		if uidErr != nil || gidErr != nil {
			closeErr := file.Close()
			return nil, errors.Join(fmt.Errorf("parse passwd ids on line %q", line), closeErr)
		}
		entries = append(entries, passwdEntry{
			name: fields[0],
			uid:  uint32(uid),
			gid:  uint32(gid),
		})
	}
	scanErr := scanner.Err()
	closeErr := file.Close()
	if scanErr != nil || closeErr != nil {
		return nil, errors.Join(
			wrapIfErr(scanErr, "scan passwd file", path),
			wrapIfErr(closeErr, "close passwd file", path),
		)
	}
	return entries, nil
}

func parseGroup(path string) ([]groupEntry, error) {
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("open group file %q: %w", path, err)
	}

	var entries []groupEntry
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) < 3 {
			continue
		}
		gid, gidErr := strconv.ParseUint(fields[2], 10, 32)
		if gidErr != nil {
			closeErr := file.Close()
			return nil, errors.Join(fmt.Errorf("parse group gid on line %q", line), closeErr)
		}
		entries = append(entries, groupEntry{
			name: fields[0],
			gid:  uint32(gid),
		})
	}
	scanErr := scanner.Err()
	closeErr := file.Close()
	if scanErr != nil || closeErr != nil {
		return nil, errors.Join(
			wrapIfErr(scanErr, "scan group file", path),
			wrapIfErr(closeErr, "close group file", path),
		)
	}
	return entries, nil
}

func wrapIfErr(err error, action, path string) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s %q: %w", action, path, err)
}

func findUserByName(entries []passwdEntry, name string) (passwdEntry, bool) {
	for _, entry := range entries {
		if entry.name == name {
			return entry, true
		}
	}
	return passwdEntry{}, false
}

func findUserByUID(entries []passwdEntry, uid uint32) (passwdEntry, bool) {
	for _, entry := range entries {
		if entry.uid == uid {
			return entry, true
		}
	}
	return passwdEntry{}, false
}

func findGroupByName(entries []groupEntry, name string) (groupEntry, bool) {
	for _, entry := range entries {
		if entry.name == name {
			return entry, true
		}
	}
	return groupEntry{}, false
}

func findGroupByGID(entries []groupEntry, gid uint32) (groupEntry, bool) {
	for _, entry := range entries {
		if entry.gid == gid {
			return entry, true
		}
	}
	return groupEntry{}, false
}
