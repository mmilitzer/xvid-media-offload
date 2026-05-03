package marker

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strings"
)

const expectedFirstLine = "#Xvid AutoGraph content protection system."

var rewriteRuleRe = regexp.MustCompile(`RewriteRule\s+(?:"([^"]+)"|(\S+))`)
var fileIDRe = regexp.MustCompile(`#file_id=([a-f0-9]+)`)

// Info holds parsed information from a marker file.
type Info struct {
	Path          string
	Valid         bool
	Patterns      []string          // patterns in file order
	FileIDByRel   map[string]string // pattern -> file_id
	AutographByRel map[string]int   // pattern -> autograph (0 or 1)
}

// entry tracks a parsed pattern with its metadata.
type entry struct {
	pattern         string
	fileID          string
	autographSet    bool
	autographValue  int
}

// Parse reads and parses a marker file, returning its Info.
// A marker is valid if either:
//   1) The first line is the expected Xvid AutoGraph comment, or
//   2) The file contains at least one MP4 RewriteRule and every parsed
//      entry has both a file_id and an #autograph=1 comment.
func Parse(path string) (*Info, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening marker file: %w", err)
	}
	defer f.Close()

	info := &Info{
		Path:           path,
		Valid:          false,
		FileIDByRel:    make(map[string]string),
		AutographByRel: make(map[string]int),
	}

	var pendingRules []string
	var blockAutographSet bool
	var blockAutographValue int
	var firstLineValid bool
	var lineNum int
	var entries []entry

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		if lineNum == 1 {
			if line == expectedFirstLine {
				firstLineValid = true
			}
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "#autograph=1") {
			blockAutographSet = true
			blockAutographValue = 1
			continue
		}
		if strings.HasPrefix(line, "#autograph=0") {
			blockAutographSet = true
			blockAutographValue = 0
			continue
		}

		if m := fileIDRe.FindStringSubmatch(line); m != nil {
			fileID := m[1]
			for _, pattern := range pendingRules {
				entries = append(entries, entry{
					pattern:        pattern,
					fileID:         fileID,
					autographSet:   blockAutographSet,
					autographValue: blockAutographValue,
				})
			}
			pendingRules = nil
			blockAutographSet = false
			blockAutographValue = 0
			continue
		}

		if m := rewriteRuleRe.FindStringSubmatch(line); m != nil {
			pattern := m[1]
			if pattern == "" {
				pattern = m[2]
			}
			pendingRules = append(pendingRules, pattern)
			continue
		}

		if strings.HasPrefix(line, "RewriteEngine") {
			pendingRules = nil
			blockAutographSet = false
			blockAutographValue = 0
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading marker file: %w", err)
	}

	if firstLineValid {
		info.Valid = true
	} else {
		info.Valid = validateLegacyMarker(entries)
	}

	if info.Valid {
		seen := make(map[string]bool)
		for _, e := range entries {
			info.FileIDByRel[e.pattern] = e.fileID
			if e.autographSet {
				info.AutographByRel[e.pattern] = e.autographValue
			}
			if !seen[e.pattern] {
				seen[e.pattern] = true
				info.Patterns = append(info.Patterns, e.pattern)
			}
		}
	}

	return info, nil
}

// validateLegacyMarker checks whether a marker without the expected first
// line is still valid under the legacy rules.
func validateLegacyMarker(entries []entry) bool {
	if len(entries) == 0 {
		return false
	}
	hasMP4 := false
	for _, e := range entries {
		if !e.autographSet || e.autographValue != 1 {
			return false
		}
		if isMP4Pattern(e.pattern) {
			hasMP4 = true
		}
	}
	return hasMP4
}

// isMP4Pattern checks whether a RewriteRule pattern targets an MP4 file.
func isMP4Pattern(pattern string) bool {
	s := pattern
	if strings.HasPrefix(s, "^") {
		s = s[1:]
	}
	if strings.HasSuffix(s, "$") {
		s = s[:len(s)-1]
	}
	return strings.HasSuffix(strings.ToLower(s), ".mp4")
}

// MatchFileID returns the file_id for a given relative file path.
// It iterates over patterns in reverse file order so that the last
// occurrence in the marker file wins.
func (info *Info) MatchFileID(relPath string) (string, bool) {
	for i := len(info.Patterns) - 1; i >= 0; i-- {
		pattern := info.Patterns[i]
		re, err := regexp.Compile(pattern)
		if err != nil {
			continue
		}
		if re.MatchString(relPath) {
			return info.FileIDByRel[pattern], true
		}
	}
	return "", false
}

// MatchAutograph returns the autograph value (0 or 1) for a given relative
// file path. It iterates over patterns in reverse file order so that the last
// occurrence in the marker file wins.
func (info *Info) MatchAutograph(relPath string) (int, bool) {
	for i := len(info.Patterns) - 1; i >= 0; i-- {
		pattern := info.Patterns[i]
		re, err := regexp.Compile(pattern)
		if err != nil {
			continue
		}
		if re.MatchString(relPath) {
			val, ok := info.AutographByRel[pattern]
			return val, ok
		}
	}
	return 0, false
}
