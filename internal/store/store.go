package store

import (
	"database/sql"
	"fmt"
	"time"
)

// schema adds sticky-refinery's own tables to the DB.
// The overseer schema (tasks table) is already applied by overseer.OpenDB().
const schema = `
CREATE TABLE IF NOT EXISTS target_files (
	path              TEXT PRIMARY KEY,
	pipeline_name     TEXT NOT NULL,
	status            TEXT NOT NULL DEFAULT 'queued',
	error_count       INTEGER NOT NULL DEFAULT 0,
	error_message     TEXT,
	queued_at         TEXT,
	started_at        TEXT,
	completed_at      TEXT,
	last_attempted_at TEXT
);

CREATE TABLE IF NOT EXISTS pipeline_config (
	name       TEXT PRIMARY KEY,
	extra_json TEXT NOT NULL DEFAULT '{}'
);
`

// Store is the sticky-refinery data access layer.
type Store struct {
	db *sql.DB
}

// New applies the sticky-refinery schema to db and returns a Store.
// db must already have the overseer schema applied.
func New(db *sql.DB) (*Store, error) {
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &Store{db: db}, nil
}

// DB returns the underlying *sql.DB for sharing with overseer.
func (s *Store) DB() *sql.DB { return s.db }

// TargetFile mirrors a row in target_files.
type TargetFile struct {
	Path            string
	PipelineName    string
	Status          string
	ErrorCount      int
	ErrorMessage    string
	QueuedAt        time.Time
	StartedAt       *time.Time
	CompletedAt     *time.Time
	LastAttemptedAt *time.Time
}

// UpsertQueued inserts or re-queues a target file.
func (s *Store) UpsertQueued(path, pipeline string) error {
	_, err := s.db.Exec(`
		INSERT INTO target_files (path, pipeline_name, status, queued_at)
		VALUES (?, ?, 'queued', ?)
		ON CONFLICT(path) DO UPDATE SET
			status = CASE WHEN excluded.status = 'queued' THEN 'queued' ELSE status END,
			queued_at = CASE WHEN status = 'errored' OR status = 'paused' THEN ? ELSE queued_at END
	`, path, pipeline, now(), now())
	return err
}

// MarkInFlight marks a task as in_flight.
func (s *Store) MarkInFlight(path string) error {
	_, err := s.db.Exec(`
		UPDATE target_files
		SET status = 'in_flight', started_at = ?, last_attempted_at = ?
		WHERE path = ?
	`, now(), now(), path)
	return err
}

// MarkCompleted marks a task as completed.
func (s *Store) MarkCompleted(path string) error {
	_, err := s.db.Exec(`
		UPDATE target_files
		SET status = 'completed', completed_at = ?
		WHERE path = ?
	`, now(), path)
	return err
}

// MarkErrored increments error_count and records the error message.
func (s *Store) MarkErrored(path, message string) error {
	_, err := s.db.Exec(`
		UPDATE target_files
		SET status = 'errored', error_count = error_count + 1, error_message = ?, last_attempted_at = ?
		WHERE path = ?
	`, message, now(), path)
	return err
}

// MarkPaused sets status to paused.
func (s *Store) MarkPaused(path string) error {
	_, err := s.db.Exec(`UPDATE target_files SET status = 'paused' WHERE path = ?`, path)
	return err
}

// MarkResumed clears paused/errored status back to queued.
func (s *Store) MarkResumed(path string) error {
	_, err := s.db.Exec(`
		UPDATE target_files SET status = 'queued', error_message = NULL WHERE path = ?
	`, path)
	return err
}

// GetByPath returns the TargetFile for path, or sql.ErrNoRows.
func (s *Store) GetByPath(path string) (*TargetFile, error) {
	row := s.db.QueryRow(`
		SELECT path, pipeline_name, status, error_count, COALESCE(error_message,''),
		       COALESCE(queued_at,''), COALESCE(started_at,''), COALESCE(completed_at,''), COALESCE(last_attempted_at,'')
		FROM target_files WHERE path = ?
	`, path)
	return scanTargetFile(row)
}

// ListTasks returns tasks filtered by pipeline / status with pagination.
func (s *Store) ListTasks(pipeline, status string, limit, offset int) ([]*TargetFile, error) {
	q := `SELECT path, pipeline_name, status, error_count, COALESCE(error_message,''),
	             COALESCE(queued_at,''), COALESCE(started_at,''), COALESCE(completed_at,''), COALESCE(last_attempted_at,'')
	      FROM target_files WHERE 1=1`
	var args []any
	if pipeline != "" {
		q += " AND pipeline_name = ?"
		args = append(args, pipeline)
	}
	if status != "" {
		q += " AND status = ?"
		args = append(args, status)
	}
	q += " ORDER BY COALESCE(queued_at,'') DESC"
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d OFFSET %d", limit, offset)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*TargetFile
	for rows.Next() {
		tf, err := scanTargetFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, tf)
	}
	return out, rows.Err()
}

// PipelineStats holds aggregate counts per pipeline.
type PipelineStats struct {
	Queued    int
	InFlight  int
	Completed int
	Errored   int
	Paused    int
}

// GetPipelineStats returns aggregated status counts for a pipeline.
func (s *Store) GetPipelineStats(pipeline string) (*PipelineStats, error) {
	rows, err := s.db.Query(`
		SELECT status, COUNT(*) FROM target_files
		WHERE pipeline_name = ? GROUP BY status
	`, pipeline)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var st PipelineStats
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		switch status {
		case "queued":
			st.Queued = count
		case "in_flight":
			st.InFlight = count
		case "completed":
			st.Completed = count
		case "errored":
			st.Errored = count
		case "paused":
			st.Paused = count
		}
	}
	return &st, rows.Err()
}

// GetPipelineExtra returns the stored extra_json for a pipeline (or "{}").
func (s *Store) GetPipelineExtra(name string) (string, error) {
	var extra string
	err := s.db.QueryRow(`SELECT extra_json FROM pipeline_config WHERE name = ?`, name).Scan(&extra)
	if err == sql.ErrNoRows {
		return "{}", nil
	}
	return extra, err
}

// SetPipelineExtra upserts extra_json for a pipeline.
func (s *Store) SetPipelineExtra(name, extraJSON string) error {
	_, err := s.db.Exec(`
		INSERT INTO pipeline_config (name, extra_json) VALUES (?, ?)
		ON CONFLICT(name) DO UPDATE SET extra_json = excluded.extra_json
	`, name, extraJSON)
	return err
}

// scanner interface so scanTargetFile works for both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanTargetFile(s scanner) (*TargetFile, error) {
	var tf TargetFile
	var queuedAt, startedAt, completedAt, lastAttemptedAt string
	err := s.Scan(
		&tf.Path, &tf.PipelineName, &tf.Status, &tf.ErrorCount, &tf.ErrorMessage,
		&queuedAt, &startedAt, &completedAt, &lastAttemptedAt,
	)
	if err != nil {
		return nil, err
	}
	if t, err := parseTime(queuedAt); err == nil {
		tf.QueuedAt = t
	}
	if startedAt != "" {
		if t, err := parseTime(startedAt); err == nil {
			tf.StartedAt = &t
		}
	}
	if completedAt != "" {
		if t, err := parseTime(completedAt); err == nil {
			tf.CompletedAt = &t
		}
	}
	if lastAttemptedAt != "" {
		if t, err := parseTime(lastAttemptedAt); err == nil {
			tf.LastAttemptedAt = &t
		}
	}
	return &tf, nil
}

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func parseTime(s string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, s)
}
