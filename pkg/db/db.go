package db

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// DB wraps a SQLite connection for offloaded file records.
type DB struct {
	conn *sql.DB
}

// Open opens or creates the SQLite database at path and ensures the schema.
func Open(path string) (*DB, error) {
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("opening sqlite db: %w", err)
	}

	db := &DB{conn: conn}
	if err := db.createTable(); err != nil {
		conn.Close()
		return nil, err
	}
	if err := db.migrateOwnershipColumns(); err != nil {
		conn.Close()
		return nil, err
	}

	return db, nil
}

func (db *DB) createTable() error {
	_, err := db.conn.Exec(`
		CREATE TABLE IF NOT EXISTS offloaded_files (
			local_path TEXT PRIMARY KEY,
			remote_file_id TEXT NOT NULL,
			autograph INTEGER NOT NULL CHECK (autograph IN (0, 1)),
			original_size INTEGER NOT NULL,
			offloaded_at_unix INTEGER NOT NULL,
			original_uid INTEGER,
			original_gid INTEGER,
			original_mode INTEGER
		)
	`)
	return err
}

func (db *DB) migrateOwnershipColumns() error {
	// SQLite does not support IF NOT EXISTS for ALTER TABLE ADD COLUMN,
	// so we catch the duplicate-column error and ignore it.
	columns := []string{
		"ALTER TABLE offloaded_files ADD COLUMN original_uid INTEGER",
		"ALTER TABLE offloaded_files ADD COLUMN original_gid INTEGER",
		"ALTER TABLE offloaded_files ADD COLUMN original_mode INTEGER",
	}
	for _, stmt := range columns {
		if _, err := db.conn.Exec(stmt); err != nil {
			if !isDuplicateColumnError(err) {
				return fmt.Errorf("migrating schema: %w", err)
			}
		}
	}
	return nil
}

func isDuplicateColumnError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	// modernc.org/sqlite returns this message for duplicate columns.
	return contains(msg, "duplicate column name") || contains(msg, "already exists")
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsAt(s, substr))
}

func containsAt(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// UpsertOffloadedFile inserts or updates a record for an offloaded file.
func (db *DB) UpsertOffloadedFile(localPath, remoteFileID string, autograph int, originalSize int64, offloadedAt time.Time, originalUID, originalGID int, originalMode uint32) error {
	_, err := db.conn.Exec(`
		INSERT INTO offloaded_files (local_path, remote_file_id, autograph, original_size, offloaded_at_unix, original_uid, original_gid, original_mode)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(local_path) DO UPDATE SET
			remote_file_id = excluded.remote_file_id,
			autograph = excluded.autograph,
			original_size = excluded.original_size,
			offloaded_at_unix = excluded.offloaded_at_unix,
			original_uid = excluded.original_uid,
			original_gid = excluded.original_gid,
			original_mode = excluded.original_mode
	`, localPath, remoteFileID, autograph, originalSize, offloadedAt.Unix(), originalUID, originalGID, originalMode)
	return err
}

// GetOffloadedFile retrieves a record by local path.
func (db *DB) GetOffloadedFile(localPath string) (remoteFileID string, autograph int, originalSize int64, offloadedAt time.Time, originalUID, originalGID int, originalMode uint32, err error) {
	row := db.conn.QueryRow(`
		SELECT remote_file_id, autograph, original_size, offloaded_at_unix, original_uid, original_gid, original_mode
		FROM offloaded_files
		WHERE local_path = ?
	`, localPath)
	var offloadedAtUnix int64
	var uid, gid sql.NullInt64
	var mode sql.NullInt64
	err = row.Scan(&remoteFileID, &autograph, &originalSize, &offloadedAtUnix, &uid, &gid, &mode)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", 0, 0, time.Time{}, 0, 0, 0, fmt.Errorf("not found")
		}
		return "", 0, 0, time.Time{}, 0, 0, 0, err
	}
	if uid.Valid {
		originalUID = int(uid.Int64)
	}
	if gid.Valid {
		originalGID = int(gid.Int64)
	}
	if mode.Valid {
		originalMode = uint32(mode.Int64)
	}
	return remoteFileID, autograph, originalSize, time.Unix(offloadedAtUnix, 0), originalUID, originalGID, originalMode, nil
}

// DeleteOffloadedFile removes a record by local path.
func (db *DB) DeleteOffloadedFile(localPath string) error {
	_, err := db.conn.Exec(`DELETE FROM offloaded_files WHERE local_path = ?`, localPath)
	return err
}

// OffloadedRecord represents a single row from the offloaded_files table.
type OffloadedRecord struct {
	LocalPath     string
	RemoteFileID  string
	Autograph     int
	OriginalSize  int64
	OffloadedAt   time.Time
}

// ListAllOffloadedFiles returns all records in the offloaded_files table.
func (db *DB) ListAllOffloadedFiles() ([]OffloadedRecord, error) {
	rows, err := db.conn.Query(`
		SELECT local_path, remote_file_id, autograph, original_size, offloaded_at_unix
		FROM offloaded_files
		ORDER BY local_path
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []OffloadedRecord
	for rows.Next() {
		var r OffloadedRecord
		var offloadedAtUnix int64
		err := rows.Scan(&r.LocalPath, &r.RemoteFileID, &r.Autograph, &r.OriginalSize, &offloadedAtUnix)
		if err != nil {
			return nil, err
		}
		r.OffloadedAt = time.Unix(offloadedAtUnix, 0)
		records = append(records, r)
	}
	return records, rows.Err()
}

// Close closes the database connection.
func (db *DB) Close() error {
	return db.conn.Close()
}
