package marker

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strings"
)

const expectedFirstLine = "#Xvid AutoGraph content protection system."

var rewriteRuleRe = regexp.MustCompile(`RewriteRule\s+"([^"]+)"`)
var fileIDRe = regexp.MustCompile(`#file_id=([a-f0-9]+)`)

// Info holds parsed information from a marker file.
type Info struct {
	Path        string
	Valid       bool
	Patterns    []string          // patterns in file order
	FileIDByRel map[string]string // pattern -> file_id
}

// Parse reads and parses a marker file, returning its Info.
// If the first line is not valid, it returns an Info with Valid=false and no error.
func Parse(path string) (*Info, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening marker file: %w", err)
	}
	defer f.Close()

	info := &Info{
		Path:        path,
		Valid:       false,
		FileIDByRel: make(map[string]string),
	}

	var pendingRules []string

	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		if lineNum == 1 {
			if line != expectedFirstLine {
				// Invalid marker; return without error but Valid=false.
				return info, nil
			}
			info.Valid = true
		}
		if !info.Valid {
			continue
		}
		if err := parseLine(line, info, &pendingRules); err != nil {
			return nil, fmt.Errorf("marker file %s line %d: %w", path, lineNum, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading marker file: %w", err)
	}
	return info, nil
}

func parseLine(line string, info *Info, pendingRules *[]string) error {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}

	// Check for file_id comment.
	if m := fileIDRe.FindStringSubmatch(line); m != nil {
		fileID := m[1]
		for _, pattern := range *pendingRules {
			if _, ok := info.FileIDByRel[pattern]; !ok {
				info.Patterns = append(info.Patterns, pattern)
			}
			info.FileIDByRel[pattern] = fileID
		}
		*pendingRules = nil
		return nil
	}

	// Check for RewriteRule.
	if m := rewriteRuleRe.FindStringSubmatch(line); m != nil {
		pattern := m[1]
		*pendingRules = append(*pendingRules, pattern)
		return nil
	}

	// RewriteEngine on starts a new block; clear pending rules.
	if strings.HasPrefix(line, "RewriteEngine") {
		*pendingRules = nil
	}

	return nil
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
