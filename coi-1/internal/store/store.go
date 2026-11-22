// internal/store/store.go

// Package store provides SQLite persistence and transactional state changes.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"coi-1/internal/conflict"
	"coi-1/internal/data"
	"coi-1/migrations"

	// modernc.org/sqlite — pure-Go драйвер. Регистрируется под именем "sqlite".
	// Не требует CGO, не требует gcc, работает на Windows «из коробки».
	_ "modernc.org/sqlite"
)

var (
	ErrInvalidTransition = errors.New("invalid transition")
	ErrConflict          = errors.New("order conflict")
	ErrNotFound          = errors.New("not found")
)

type Store struct {
	db *sql.DB
}

type AuditRow struct {
	AuditID     int64           `json:"audit_id"`
	OccurredAt  string          `json:"occurred_at"`
	OrderID     string          `json:"order_id"`
	Action      string          `json:"action"`
	ActorID     *string         `json:"actor_id"`
	DLTOID      *string         `json:"dlto_id"`
	PersonID    *string         `json:"person_id"`
	RuleVersion string          `json:"rule_version"`
	LatencyMS   *int64          `json:"latency_ms"`
	SMEVStatus  *string         `json:"smev_status"`
	SignalJSON  json.RawMessage `json:"signal_json"`
	Comment     *string         `json:"comment"`
}

type Decision struct {
	Decision  string `json:"decision"`
	Reviewer  string `json:"reviewer_id"`
	Comment   string `json:"comment"`
	NewDLTOID string `json:"new_dlto_id,omitempty"`
}

// Open открывает SQLite в файле path.
//
// Ключевые моменты для Windows:
//   - filepath.ToSlash превращает `C:\Users\...\coi.db` в `C:/Users/.../coi.db`;
//     URI-форма `file:` на Windows плохо переносит обратные слэши.
//   - PRAGMA передаются через `?_pragma=name(value)` — синтаксис modernc,
//     а не mattn. Поэтому DSN отличается от предыдущей версии.
//   - `_txlock=immediate` просит драйвер начинать транзакции с BEGIN IMMEDIATE.
//     Это защищает от гонок `INSERT ... SELECT` при параллельных проверках.
func Open(path string) (*Store, error) {
	dsn := "file:" + filepath.ToSlash(path) +
		"?_pragma=foreign_keys(1)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(250)" +
		"&_txlock=immediate"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Ready(ctx context.Context) error {
	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM person").Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return errors.New("dataset is empty")
	}
	return nil
}

func (s *Store) Migrate(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (version INTEGER PRIMARY KEY)`); err != nil {
		return err
	}
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_version WHERE version=1").Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, migrations.Initial); err != nil {
		return fmt.Errorf("apply migration: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO schema_version(version) VALUES(1)"); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Seed(ctx context.Context, b data.Bundle) error {
	if err := b.Validate(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM person").Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return errors.New("seed requires an empty registry")
	}
	for _, p := range b.Persons {
		raw, err := json.Marshal(p)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO person(person_id,role,payload) VALUES(?,?,?)",
			p.ID, p.Role, string(raw)); err != nil {
			return err
		}
		if p.Passport != "" {
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO passport(person_id,number) VALUES(?,?)", p.ID, p.Passport); err != nil {
				return err
			}
		}
	}
	for _, c := range b.Civil {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO civil_registry(record_id,a,b,kind) VALUES(?,?,?,?)",
			c.ID, c.A, c.B, c.Kind); err != nil {
			return err
		}
	}
	for _, k := range b.Kinship {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO kinship(a,b,kind,civil_id) VALUES(?,?,?,NULLIF(?,''))",
			k.A, k.B, k.Kind, k.CivilID); err != nil {
			return err
		}
	}
	for _, d := range b.Divorces {
		if _, err := tx.ExecContext(ctx, "INSERT INTO divorce(a,b) VALUES(?,?)", d.A, d.B); err != nil {
			return err
		}
	}
	for _, g := range b.Guardianships {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO guardianship(guardian,ward) VALUES(?,?)", g.Guardian, g.Ward); err != nil {
			return err
		}
	}
	for _, e := range b.Emancipations {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO emancipation(person_id) VALUES(?)", e.PersonID); err != nil {
			return err
		}
	}
	for _, p := range b.Parental {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO parental_rights(person_id,child_id) VALUES(?,?)", p.PersonID, p.ChildID); err != nil {
			return err
		}
	}
	for _, c := range b.Capacity {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO legal_capacity(person_id,state) VALUES(?,?)", c.PersonID, c.State); err != nil {
			return err
		}
	}
	for _, e := range b.Employment {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO employment_service(person_id,unemployed) VALUES(?,?)",
			e.PersonID, e.Unemployed); err != nil {
			return err
		}
	}
	for _, x := range b.Tax {
		raw, err := json.Marshal(x.OGRNIP)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO tax_registration(person_id,ogrnip_json) VALUES(?,?)",
			x.PersonID, string(raw)); err != nil {
			return err
		}
	}
	for _, d := range b.DriverLicenses {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO driver_license(person_id,number) VALUES(?,?)", d.PersonID, d.Number); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// People реализует identity.Registry.
func (s *Store) People(ctx context.Context) ([]data.Person, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT payload FROM person ORDER BY person_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]data.Person, 0)
	for rows.Next() {
		var raw string
		var p data.Person
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func transition(current, next string) error {
	valid := current == "draft" && next == "pending_review" ||
		current == "pending_review" &&
			(next == "approved" || next == "reassigned" || next == "cancelled") ||
		current == "reassigned" && next == "draft"
	if !valid {
		return ErrInvalidTransition
	}
	return nil
}

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func event(ctx context.Context, tx *sql.Tx, now time.Time, orderID, action string,
	actor, dlto string, person *string, version string, latency *int64,
	status string, signal []byte, comment string) (int64, error) {
	var snapshot any
	if len(signal) != 0 {
		snapshot = string(signal)
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO audit_log(
		occurred_at,order_id,action,actor_id,dlto_id,person_id,rule_version,
		latency_ms,smev_status,signal_json,comment
	) VALUES(?,?,?,NULLIF(?,''),NULLIF(?,''),?,?,?,NULLIF(?,''),?,NULLIF(?,''))`,
		stamp(now), orderID, action, actor, dlto, person, version,
		latency, status, snapshot, comment)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) StartCheck(ctx context.Context, id, dlto, personRef, version string,
	now time.Time) (string, error) {
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", err
	}
	token := hex.EncodeToString(tokenBytes)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	var role string
	if err := tx.QueryRowContext(ctx, "SELECT role FROM person WHERE person_id=?", dlto).Scan(&role); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	if role != "DLTO" {
		return "", ErrNotFound
	}
	var status, savedDLTO, lock string
	err = tx.QueryRowContext(ctx,
		"SELECT status,dlto_id,COALESCE(check_token,'') FROM ldt_order WHERE order_id=?", id).
		Scan(&status, &savedDLTO, &lock)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.ExecContext(ctx, `INSERT INTO ldt_order(
			order_id,created_at,dlto_id,person_ref,status,rule_version,check_token
		) VALUES(?,?,?,?,'draft',?,?)`,
			id, stamp(now), dlto, personRef, version, token); err != nil {
			return "", err
		}
	case err != nil:
		return "", err
	default:
		if status != "draft" || lock != "" || savedDLTO != dlto {
			return "", ErrConflict
		}
		if _, err := tx.ExecContext(ctx, `UPDATE ldt_order
			SET person_ref=?,rule_version=?,check_token=? WHERE order_id=?`,
			personRef, version, token, id); err != nil {
			return "", err
		}
	}
	if _, err := event(ctx, tx, now, id, "check_started", "", dlto, nil,
		version, nil, "", nil, ""); err != nil {
		return "", err
	}
	return token, tx.Commit()
}

func (s *Store) CompleteCheck(ctx context.Context, token string, signal conflict.Signal,
	latency int64, status string) (int64, error) {
	raw, err := json.Marshal(signal)
	if err != nil {
		return 0, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var current string
	if err := tx.QueryRowContext(ctx, `SELECT status FROM ldt_order
		WHERE order_id=? AND check_token=?`, signal.OrderID, token).Scan(&current); err != nil {
		return 0, err
	}
	if err := transition(current, "pending_review"); err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE ldt_order SET
		checked_at=?,person_id=?,status='pending_review',signal_json=?,
		rule_version=?,check_token=NULL
		WHERE order_id=? AND check_token=?`,
		stamp(signal.GeneratedAt), signal.PersonID, string(raw),
		signal.RuleVersion, signal.OrderID, token)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if n != 1 {
		return 0, ErrConflict
	}
	id, err := event(ctx, tx, signal.GeneratedAt, signal.OrderID, "check_completed",
		"", signal.DLTOID, signal.PersonID, signal.RuleVersion, &latency,
		status, raw, "")
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

func (s *Store) FailCheck(ctx context.Context, orderID, token, status, version string,
	now time.Time, latency int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var dlto string
	if err := tx.QueryRowContext(ctx,
		"SELECT dlto_id FROM ldt_order WHERE order_id=? AND check_token=?",
		orderID, token).Scan(&dlto); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		"UPDATE ldt_order SET check_token=NULL WHERE order_id=? AND check_token=?",
		orderID, token); err != nil {
		return err
	}
	if _, err := event(ctx, tx, now, orderID, "check_failed", "", dlto, nil,
		version, &latency, status, nil, "Проверка не завершена"); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Decide(ctx context.Context, orderID string, d Decision, now time.Time) (int64, error) {
	if strings.TrimSpace(d.Reviewer) == "" || strings.TrimSpace(d.Comment) == "" {
		return 0, errors.New("reviewer and comment are required")
	}
	if d.Decision != "approved" && d.Decision != "reassigned" && d.Decision != "cancelled" {
		return 0, ErrInvalidTransition
	}
	if d.Decision == "reassigned" && strings.TrimSpace(d.NewDLTOID) == "" {
		return 0, errors.New("new official is required")
	}
	if d.Decision != "reassigned" && d.NewDLTOID != "" {
		return 0, errors.New("new official is only valid for reassignment")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var current, dlto, raw, version string
	var person *string
	err = tx.QueryRowContext(ctx, `SELECT status,dlto_id,person_id,
		COALESCE(signal_json,''),COALESCE(rule_version,'')
		FROM ldt_order WHERE order_id=?`, orderID).
		Scan(&current, &dlto, &person, &raw, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	if err := transition(current, d.Decision); err != nil {
		return 0, err
	}
	if d.Decision == "reassigned" {
		var role string
		if err := tx.QueryRowContext(ctx,
			"SELECT role FROM person WHERE person_id=?", d.NewDLTOID).Scan(&role); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return 0, ErrNotFound
			}
			return 0, err
		}
		if role != "DLTO" || d.NewDLTOID == dlto {
			return 0, errors.New("invalid replacement official")
		}
	}
	var signal conflict.Signal
	if err := json.Unmarshal([]byte(raw), &signal); err != nil {
		return 0, err
	}
	status := "ok"
	if signal.Severity == conflict.SeverityNoData {
		status = "no_data"
	}
	auditID, err := event(ctx, tx, now, orderID, d.Decision, d.Reviewer, dlto,
		person, version, nil, status, []byte(raw), d.Comment)
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE ldt_order SET status=?,
		reviewer_id=?,reviewer_comment=?,decided_at=? WHERE order_id=?`,
		d.Decision, d.Reviewer, d.Comment, stamp(now), orderID); err != nil {
		return 0, err
	}
	if d.Decision == "reassigned" {
		if err := transition("reassigned", "draft"); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE ldt_order SET
			status='draft',dlto_id=?,person_id=NULL,signal_json=NULL,
			checked_at=NULL,reviewer_id=NULL,reviewer_comment=NULL,decided_at=NULL
			WHERE order_id=?`, d.NewDLTOID, orderID); err != nil {
			return 0, err
		}
	}
	return auditID, tx.Commit()
}

// Audit реализует audit.Repository.
func (s *Store) Audit(ctx context.Context, orderID string) ([]AuditRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT audit_id,occurred_at,order_id,
		action,actor_id,dlto_id,person_id,rule_version,latency_ms,
		smev_status,signal_json,comment FROM audit_log
		WHERE order_id=? ORDER BY audit_id`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]AuditRow, 0)
	for rows.Next() {
		var a AuditRow
		var raw *string
		if err := rows.Scan(&a.AuditID, &a.OccurredAt, &a.OrderID, &a.Action,
			&a.ActorID, &a.DLTOID, &a.PersonID, &a.RuleVersion,
			&a.LatencyMS, &a.SMEVStatus, &raw, &a.Comment); err != nil {
			return nil, err
		}
		if raw != nil {
			a.SignalJSON = json.RawMessage(*raw)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
