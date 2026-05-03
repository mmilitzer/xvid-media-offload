package credentials

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const credentialFilename = "cmsinclude.ini.php"

// Credentials holds HMAC signing credentials for a scan root.
type Credentials struct {
	ClientID     string
	ClientSecret string
}

// FindForPath walks upward from the given path through parent directories
// and returns the credentials from the first found credential file.
func FindForPath(startPath string) (*Credentials, string, error) {
	dir := filepath.Clean(startPath)
	if info, err := os.Stat(dir); err == nil && !info.IsDir() {
		dir = filepath.Dir(dir)
	}

	for {
		credPath := filepath.Join(dir, credentialFilename)
		if info, err := os.Stat(credPath); err == nil && !info.IsDir() {
			creds, err := Parse(credPath)
			if err != nil {
				return nil, credPath, fmt.Errorf("parsing %s: %w", credPath, err)
			}
			return creds, credPath, nil
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	return nil, "", fmt.Errorf("no %s found in any parent of %s", credentialFilename, startPath)
}

// Parse reads credentials from a PHP INI-style file.
func Parse(path string) (*Credentials, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening credential file: %w", err)
	}
	defer f.Close()

	var creds Credentials
	inXvidSection := false

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "#") {
			continue
		}

		if line == "[xvid]" {
			inXvidSection = true
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			inXvidSection = false
			continue
		}

		if inXvidSection {
			key, val, ok := parseINIKeyValue(line)
			if !ok {
				continue
			}
			switch key {
			case "APP_CLIENT_ID":
				creds.ClientID = val
			case "APP_CLIENT_SECRET":
				creds.ClientSecret = val
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading credential file: %w", err)
	}

	if creds.ClientID == "" || creds.ClientSecret == "" {
		return nil, fmt.Errorf("missing APP_CLIENT_ID or APP_CLIENT_SECRET in %s", path)
	}

	return &creds, nil
}

func parseINIKeyValue(line string) (key, val string, ok bool) {
	parts := strings.SplitN(line, "=", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	key = strings.TrimSpace(parts[0])
	val = strings.TrimSpace(parts[1])
	val = strings.Trim(val, `"'`)
	return key, val, true
}
