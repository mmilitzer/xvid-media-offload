package credentials

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseValidINI(t *testing.T) {
	content := `[xvid]
APP_CLIENT_ID = "test-id-123"
APP_CLIENT_SECRET = "dGVzdC1zZWNyZXQ="
`
	path := filepath.Join(t.TempDir(), "cmsinclude.ini.php")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	creds, err := Parse(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if creds.ClientID != "test-id-123" {
		t.Errorf("unexpected client id: %s", creds.ClientID)
	}
	if creds.ClientSecret != "dGVzdC1zZWNyZXQ=" {
		t.Errorf("unexpected client secret: %s", creds.ClientSecret)
	}
}

func TestParseMissingSection(t *testing.T) {
	content := `[other]
APP_CLIENT_ID = "test-id"
APP_CLIENT_SECRET = "secret"
`
	path := filepath.Join(t.TempDir(), "cmsinclude.ini.php")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := Parse(path)
	if err == nil {
		t.Error("expected error for missing [xvid] section")
	}
}

func TestParseMissingCredentials(t *testing.T) {
	content := `[xvid]
APP_CLIENT_ID = "test-id"
`
	path := filepath.Join(t.TempDir(), "cmsinclude.ini.php")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := Parse(path)
	if err == nil {
		t.Error("expected error for missing client secret")
	}
}

func TestFindForPathWalkUp(t *testing.T) {
	tmpDir := t.TempDir()
	content := `[xvid]
APP_CLIENT_ID = "found-id"
APP_CLIENT_SECRET = "found-secret"
`
	if err := os.WriteFile(filepath.Join(tmpDir, credentialFilename), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	subDir := filepath.Join(tmpDir, "content", "group1", "set1")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	creds, foundPath, err := FindForPath(subDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if creds.ClientID != "found-id" {
		t.Errorf("unexpected client id: %s", creds.ClientID)
	}
	if foundPath != filepath.Join(tmpDir, credentialFilename) {
		t.Errorf("unexpected path: %s", foundPath)
	}
}

func TestFindForPathNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "content", "group1")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	_, _, err := FindForPath(subDir)
	if err == nil {
		t.Error("expected error when credential file not found")
	}
}
