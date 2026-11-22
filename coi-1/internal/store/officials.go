// internal/store/officials.go

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"coi-1/internal/data"
)

// ErrOfficialInUse — должностное лицо уже указано в поручениях, удалять нельзя:
// это разрушит ссылочную целостность журнала.
var ErrOfficialInUse = errors.New("official is referenced by orders")

// Officials возвращает должностных лиц из журнала.
func (s *Store) Officials(ctx context.Context) ([]data.Person, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT payload FROM person WHERE role='DLTO' ORDER BY person_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]data.Person, 0)
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var p data.Person
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			return nil, fmt.Errorf("decode official payload: %w", err)
		}
		p.Role = "DLTO"
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpsertOfficial создаёт или обновляет должностное лицо.
//
// ON CONFLICT используется вместо INSERT OR REPLACE: replace удаляет строку и
// вставляет заново, а на person(person_id) ссылаются поручения.
func (s *Store) UpsertOfficial(ctx context.Context, p data.Person) error {
	if p.ID == "" {
		return errors.New("official id is required")
	}
	p.Role = "DLTO"

	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `INSERT INTO person(person_id, role, payload)
		VALUES(?, 'DLTO', ?)
		ON CONFLICT(person_id) DO UPDATE SET role='DLTO', payload=excluded.payload`,
		p.ID, string(raw)); err != nil {
		return fmt.Errorf("upsert official: %w", err)
	}

	if p.Passport != "" {
		if _, err := tx.ExecContext(ctx, `INSERT INTO passport(person_id, number)
			VALUES(?, ?)
			ON CONFLICT(person_id) DO UPDATE SET number=excluded.number`,
			p.ID, p.Passport); err != nil {
			return fmt.Errorf("upsert official passport: %w", err)
		}
	}

	return tx.Commit()
}

// DeleteOfficial удаляет должностное лицо, если на него нет ссылок.
func (s *Store) DeleteOfficial(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var used int
	if err := tx.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM ldt_order WHERE dlto_id = ?", id).Scan(&used); err != nil {
		return err
	}
	if used > 0 {
		return ErrOfficialInUse
	}

	// Паспорт ссылается на person, поэтому удаляется первым.
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM passport WHERE person_id = ?", id); err != nil {
		return err
	}

	res, err := tx.ExecContext(ctx,
		"DELETE FROM person WHERE person_id = ? AND role = 'DLTO'", id)
	if err != nil {
		return err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}
