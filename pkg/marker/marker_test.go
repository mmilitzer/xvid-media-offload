package marker

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseValidMarker(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".htaccess")
	data := `#Xvid AutoGraph content protection system.
#Removing or renaming this file will disable all content protections.
RewriteEngine on
RewriteRule "^720p/abc.mp4" - [F,L,NC]
#autograph=1
#file_id=669872d3d3586a56f9a3dfad
`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	info, err := Parse(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !info.Valid {
		t.Fatal("expected marker to be valid")
	}
	if len(info.FileIDByRel) != 1 {
		t.Errorf("expected 1 mapping, got %d", len(info.FileIDByRel))
	}
	if id, ok := info.FileIDByRel["^720p/abc.mp4"]; !ok || id != "669872d3d3586a56f9a3dfad" {
		t.Errorf("unexpected mapping: %v", info.FileIDByRel)
	}
}

func TestParseInvalidFirstLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".htaccess")
	data := `#Invalid marker
RewriteEngine on
RewriteRule "^720p/abc.mp4" - [F,L,NC]
#file_id=669872d3d3586a56f9a3dfad
`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	info, err := Parse(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Valid {
		t.Fatal("expected marker to be invalid")
	}
	if len(info.FileIDByRel) != 0 {
		t.Errorf("expected 0 mappings, got %d", len(info.FileIDByRel))
	}
}

func TestParseLegacyValidMarker(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".htaccess")
	data := `RewriteEngine on
RewriteRule "^mp4x1920/CC-ZEJ_1920.mp4" - [F,L,NC]
RewriteRule "^mp4x1920/.*\.m4s$" - [F,L,NC]
#autograph=1
#pipeline_id=58e35b4fe4b0fd711eb07aa6
#file_id=624d137ae4b04519d123e5d1
RewriteEngine on
RewriteRule "^mp4x960/CC-ZEJ_960.mp4" - [F,L,NC]
RewriteRule "^mp4x960/.*\.m4s$" - [F,L,NC]
#autograph=1
#file_id=624d137ae4b04519d123e5d3
`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	info, err := Parse(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !info.Valid {
		t.Fatal("expected legacy marker to be valid")
	}
	if len(info.FileIDByRel) != 4 {
		t.Errorf("expected 4 mappings, got %d", len(info.FileIDByRel))
	}
	if id, ok := info.FileIDByRel["^mp4x1920/CC-ZEJ_1920.mp4"]; !ok || id != "624d137ae4b04519d123e5d1" {
		t.Errorf("unexpected mapping: %v", info.FileIDByRel)
	}
}

func TestParseLegacyValidMarkerUnquoted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".htaccess")
	data := `RewriteRule ^mp4x1920/CC-2017-04-21_1920.mp4 - [F,L,NC]
#autograph=1
#pipeline_id=58e35b4fe4b0fd711eb07aa6
#file_id=58f58aa5e4b099059ab9e310
#object_key=/CC-2017-04-21/mp4x1920/CC-2017-04-21_1920.mp4
RewriteRule ^mp4x960/CC-2017-04-21_960.mp4 - [F,L,NC]
#autograph=1
#pipeline_id=58e35b4fe4b0fd711eb07aa6
#file_id=58f58aa5e4b099059ab9e311
#object_key=/CC-2017-04-21/mp4x960/CC-2017-04-21_960.mp4
RewriteRule ^mp4x480/CC-2017-04-21_480.mp4 - [F,L,NC]
#autograph=1
#pipeline_id=58e35b4fe4b0fd711eb07aa6
#file_id=58f58aa5e4b099059ab9e312
#object_key=/CC-2017-04-21/mp4x480/CC-2017-04-21_480.mp4
`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	info, err := Parse(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !info.Valid {
		t.Fatal("expected legacy marker with unquoted rules to be valid")
	}
	if len(info.FileIDByRel) != 3 {
		t.Errorf("expected 3 mappings, got %d", len(info.FileIDByRel))
	}
	if id, ok := info.FileIDByRel["^mp4x1920/CC-2017-04-21_1920.mp4"]; !ok || id != "58f58aa5e4b099059ab9e310" {
		t.Errorf("unexpected mapping: %v", info.FileIDByRel)
	}
}

func TestParseLegacyInvalidWithoutAutograph(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".htaccess")
	data := `RewriteEngine on
RewriteRule "^720p/abc.mp4" - [F,L,NC]
#file_id=669872d3d3586a56f9a3dfad
`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	info, err := Parse(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info.Valid {
		t.Fatal("expected marker to be invalid without #autograph=1")
	}
}

func TestParseMultipleResolutions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".htaccess")
	data := `#Xvid AutoGraph content protection system.
RewriteEngine on
RewriteRule "^720p/abc.mp4" - [F,L,NC]
#file_id=111111111111111111111111
RewriteEngine on
RewriteRule "^1080p/abc.mp4" - [F,L,NC]
#file_id=222222222222222222222222
`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	info, err := Parse(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !info.Valid {
		t.Fatal("expected marker to be valid")
	}
	if len(info.FileIDByRel) != 2 {
		t.Errorf("expected 2 mappings, got %d", len(info.FileIDByRel))
	}
}

func TestParseDuplicateEntriesLastWins(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".htaccess")
	data := `#Xvid AutoGraph content protection system.
RewriteEngine on
RewriteRule "^720p/abc.mp4" - [F,L,NC]
#file_id=111111111111111111111111
RewriteEngine on
RewriteRule "^720p/abc.mp4" - [F,L,NC]
#file_id=222222222222222222222222
`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	info, err := Parse(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !info.Valid {
		t.Fatal("expected marker to be valid")
	}
	id, ok := info.MatchFileID("720p/abc.mp4")
	if !ok {
		t.Fatal("expected match")
	}
	if id != "222222222222222222222222" {
		t.Errorf("expected last file_id to win, got %s", id)
	}
}

func TestMatchFileID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".htaccess")
	data := `#Xvid AutoGraph content protection system.
RewriteEngine on
RewriteRule "^720p/abc.mp4" - [F,L,NC]
#file_id=669872d3d3586a56f9a3dfad
`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	info, err := Parse(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	id, ok := info.MatchFileID("720p/abc.mp4")
	if !ok {
		t.Fatal("expected match for 720p/abc.mp4")
	}
	if id != "669872d3d3586a56f9a3dfad" {
		t.Errorf("unexpected id: %s", id)
	}

	_, ok = info.MatchFileID("720p/other.mp4")
	if ok {
		t.Error("expected no match for unrelated path")
	}
}

func TestMatchFileIDWithRegex(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".htaccess")
	data := `#Xvid AutoGraph content protection system.
RewriteEngine on
RewriteRule "^720p/.*\.mp4$" - [F,L,NC]
#file_id=669872d3d3586a56f9a3dfad
`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}

	info, err := Parse(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	id, ok := info.MatchFileID("720p/anything.mp4")
	if !ok {
		t.Fatal("expected match")
	}
	if id != "669872d3d3586a56f9a3dfad" {
		t.Errorf("unexpected id: %s", id)
	}
}
