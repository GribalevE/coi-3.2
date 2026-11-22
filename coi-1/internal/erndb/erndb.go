// internal/erndb/erndb.go

// Package erndb loads synthetic ERN dumps of either schema and converts
// them to data.Bundle for the conflict engine.
//
// Two schemas are supported:
//
//  1. "migrations" schema — person(person_id, role, payload JSON) + passport,
//     kinship(a,b,kind,civil_id) and so on. This is what data/ern_demo.sql uses.
//
//  2. "legacy ERN" schema — flat columns:
//     person(person_type, last_name, first_name, …, address, position, customs_post),
//     passport_rf(series, number), kinship(person_id_1, person_id_2, relation, act_number),
//     civil_registry(zags_id, person_id_1, person_id_2, relation, act_number), …
//     This is what ЕРН_2026-11-12.sql uses.
package erndb

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"coi-1/internal/data"
	"coi-1/migrations"

	_ "modernc.org/sqlite"
)

// Open opens either schema as an in-memory SQLite and executes the dump.
func Open(path string) (*sql.DB, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	raw = bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)

	if isLegacyERN(raw) {
		if _, err := db.Exec(string(raw)); err != nil {
			db.Close()
			return nil, fmt.Errorf("exec legacy ERN: %w", err)
		}
		return db, nil
	}

	// migrations schema — schema first, then INSERT-ы.
	if _, err := db.Exec(migrations.Initial); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply migrations: %w", err)
	}
	if _, err := db.Exec(string(raw)); err != nil {
		db.Close()
		return nil, fmt.Errorf("exec migrations dump: %w", err)
	}
	return db, nil
}

func isLegacyERN(raw []byte) bool {
	return bytes.Contains(raw, []byte("person_type TEXT"))
}

// Bundle reads the open DB and returns a validated data.Bundle.
func Bundle(ctx context.Context, db *sql.DB) (data.Bundle, error) {
	if hasColumn(db, "person", "person_type") {
		return loadLegacy(ctx, db)
	}
	return loadMigrations(ctx, db)
}

func hasColumn(db *sql.DB, table, column string) bool {
	var n int
	db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name=?`, table, column,
	).Scan(&n)
	return n > 0
}

// ---------- legacy ERN ----------

func loadLegacy(ctx context.Context, db *sql.DB) (data.Bundle, error) {
	b := data.Bundle{}

	// Passports
	passports := map[string]string{}
	if rows, err := db.QueryContext(ctx,
		`SELECT person_id, series, number FROM passport_rf WHERE is_valid = 1`); err == nil {
		for rows.Next() {
			var id, ser, num string
			rows.Scan(&id, &ser, &num)
			passports[id] = strings.TrimSpace(ser + " " + num)
		}
		rows.Close()
	}

	// Emancipated set
	emancipated := map[string]bool{}
	if rows, err := db.QueryContext(ctx, `SELECT person_id FROM emancipation`); err == nil {
		for rows.Next() {
			var id string
			rows.Scan(&id)
			emancipated[id] = true
		}
		rows.Close()
	}

	// Persons
	rows, err := db.QueryContext(ctx, `
		SELECT person_id, person_type, last_name, first_name, middle_name,
		       COALESCE(birth_date,''), COALESCE(address,''),
		       COALESCE(snils,''), COALESCE(inn,''), COALESCE(citizenship,'')
		FROM person ORDER BY person_id`)
	if err != nil {
		return b, fmt.Errorf("query person: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, role, last, first, middle, birth, address, snils, inn, citizenship string
		if err := rows.Scan(&id, &role, &last, &first, &middle, &birth,
			&address, &snils, &inn, &citizenship); err != nil {
			return b, err
		}
		city, street, house := parseAddress(address)
		b.Persons = append(b.Persons, data.Person{
			ID:           id,
			Role:         role,
			FullName:     strings.TrimSpace(last + " " + first + " " + middle),
			Passport:     passports[id],
			Address:      address,
			City:         city,
			Street:       street,
			House:        house,
			BirthDate:    birth,
			SNILS:        snils,
			INN:          inn,
			Citizenships: parseCitizenships(citizenship),
			Emancipated:  emancipated[id],
		})
	}

	// Civil registry
	type civilRow struct {
		zagsID, a, b, rel, act string
	}
	var civilRows []civilRow
	if r, err := db.QueryContext(ctx,
		`SELECT zags_id, person_id_1, person_id_2, relation, act_number FROM civil_registry`); err == nil {
		for r.Next() {
			var c civilRow
			r.Scan(&c.zagsID, &c.a, &c.b, &c.rel, &c.act)
			civilRows = append(civilRows, c)
		}
		r.Close()
	}
	for _, c := range civilRows {
		a, b2, kind := normalizeEdge(c.a, c.b, c.rel)
		if kind == "" {
			continue
		}
		b.Civil = append(b.Civil, data.Civil{ID: c.zagsID, A: a, B: b2, Kind: kind})
	}

	// Kinship + CivilID matching
	if r, err := db.QueryContext(ctx,
		`SELECT person_id_1, person_id_2, relation, act_number FROM kinship`); err == nil {
		seen := map[string]bool{}
		for r.Next() {
			var a, b2, rel, act string
			r.Scan(&a, &b2, &rel, &act)
			a, b2, kind := normalizeEdge(a, b2, rel)
			if kind == "" {
				continue
			}
			key := a + "|" + b2 + "|" + kind
			if seen[key] {
				continue
			}
			seen[key] = true
			// Find matching civil_registry record (same act_number + same direction after normalize)
			var civilID string
			for _, c := range civilRows {
				if c.act != act {
					continue
				}
				ca, cb, ck := normalizeEdge(c.a, c.b, c.rel)
				if ca == a && cb == b2 && ck == kind {
					civilID = c.zagsID
					break
				}
			}
			b.Kinship = append(b.Kinship, data.Kinship{
				A: a, B: b2, Kind: kind, CivilID: civilID,
			})
		}
		r.Close()
	}

	// Divorces
	if r, err := db.QueryContext(ctx,
		`SELECT person_id_1, person_id_2 FROM divorce`); err == nil {
		seen := map[string]bool{}
		for r.Next() {
			var a, b2 string
			r.Scan(&a, &b2)
			key := a + "|" + b2
			rev := b2 + "|" + a
			if seen[key] || seen[rev] {
				continue
			}
			seen[key] = true
			b.Divorces = append(b.Divorces, data.Divorce{A: a, B: b2})
		}
		r.Close()
	}

	// Tax
	if r, err := db.QueryContext(ctx,
		`SELECT person_id, COALESCE(ogrnip,'') FROM tax_registration`); err == nil {
		for r.Next() {
			var pid, ogrnip string
			r.Scan(&pid, &ogrnip)
			list := []string{}
			if ogrnip != "" {
				list = []string{ogrnip}
			}
			b.Tax = append(b.Tax, data.Tax{PersonID: pid, OGRNIP: list})
		}
		r.Close()
	}

	// Employment
	if r, err := db.QueryContext(ctx,
		`SELECT person_id, COALESCE(status,'') FROM employment_service`); err == nil {
		for r.Next() {
			var pid, status string
			r.Scan(&pid, &status)
			b.Employment = append(b.Employment, data.Employment{
				PersonID:   pid,
				Unemployed: strings.EqualFold(status, "безработный"),
			})
		}
		r.Close()
	}

	// Legal capacity
	if r, err := db.QueryContext(ctx,
		`SELECT person_id, COALESCE(status,'') FROM legal_capacity`); err == nil {
		for r.Next() {
			var pid, status string
			r.Scan(&pid, &status)
			state := "full"
			switch strings.ToLower(status) {
			case "недееспособен":
				state = "incapable"
			case "ограничен":
				state = "limited"
			}
			b.Capacity = append(b.Capacity, data.Capacity{PersonID: pid, State: state})
		}
		r.Close()
	}

	// Driver licenses
	if r, err := db.QueryContext(ctx,
		`SELECT person_id, COALESCE(number,'') FROM driver_license`); err == nil {
		seen := map[string]bool{}
		for r.Next() {
			var pid, num string
			r.Scan(&pid, &num)
			if num == "" || seen[pid] {
				continue
			}
			seen[pid] = true
			b.DriverLicenses = append(b.DriverLicenses, data.DriverLicense{PersonID: pid, Number: num})
		}
		r.Close()
	}

	if err := b.Validate(); err != nil {
		return b, fmt.Errorf("validate legacy ERN: %w", err)
	}
	return b, nil
}

// ---------- migrations schema ----------

func loadMigrations(ctx context.Context, db *sql.DB) (data.Bundle, error) {
	b := data.Bundle{}

	rows, err := db.QueryContext(ctx, `SELECT payload FROM person ORDER BY person_id`)
	if err != nil {
		return b, fmt.Errorf("query person: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		var p data.Person
		if err := rows.Scan(&raw); err != nil {
			return b, err
		}
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			return b, fmt.Errorf("decode person payload: %w", err)
		}
		b.Persons = append(b.Persons, p)
	}

	// Passport table (fallback fill)
	if hasColumn(db, "passport", "number") {
		byID := map[string]*data.Person{}
		for i := range b.Persons {
			byID[b.Persons[i].ID] = &b.Persons[i]
		}
		if r, err := db.QueryContext(ctx, `SELECT person_id, number FROM passport`); err == nil {
			for r.Next() {
				var id, num string
				r.Scan(&id, &num)
				if p, ok := byID[id]; ok && p.Passport == "" {
					p.Passport = num
				}
			}
			r.Close()
		}
	}

	// Civil
	if r, err := db.QueryContext(ctx, `SELECT record_id, a, b, kind FROM civil_registry`); err == nil {
		for r.Next() {
			var c data.Civil
			r.Scan(&c.ID, &c.A, &c.B, &c.Kind)
			b.Civil = append(b.Civil, c)
		}
		r.Close()
	}

	// Kinship
	if r, err := db.QueryContext(ctx,
		`SELECT a, b, kind, COALESCE(civil_id,'') FROM kinship`); err == nil {
		for r.Next() {
			var k data.Kinship
			r.Scan(&k.A, &k.B, &k.Kind, &k.CivilID)
			b.Kinship = append(b.Kinship, k)
		}
		r.Close()
	}

	// Divorce
	if r, err := db.QueryContext(ctx, `SELECT a, b FROM divorce`); err == nil {
		for r.Next() {
			var d data.Divorce
			r.Scan(&d.A, &d.B)
			b.Divorces = append(b.Divorces, d)
		}
		r.Close()
	}

	// Tax
	if r, err := db.QueryContext(ctx,
		`SELECT person_id, COALESCE(ogrnip_json,'[]') FROM tax_registration`); err == nil {
		for r.Next() {
			var id, raw string
			r.Scan(&id, &raw)
			var og []string
			if raw != "" {
				json.Unmarshal([]byte(raw), &og)
			}
			b.Tax = append(b.Tax, data.Tax{PersonID: id, OGRNIP: og})
		}
		r.Close()
	}

	// Employment
	if r, err := db.QueryContext(ctx,
		`SELECT person_id, unemployed FROM employment_service`); err == nil {
		for r.Next() {
			var pid string
			var un int
			r.Scan(&pid, &un)
			b.Employment = append(b.Employment, data.Employment{PersonID: pid, Unemployed: un != 0})
		}
		r.Close()
	}

	// Capacity
	if r, err := db.QueryContext(ctx, `SELECT person_id, state FROM legal_capacity`); err == nil {
		for r.Next() {
			var c data.Capacity
			r.Scan(&c.PersonID, &c.State)
			b.Capacity = append(b.Capacity, c)
		}
		r.Close()
	}

	// Emancipation
	if r, err := db.QueryContext(ctx, `SELECT person_id FROM emancipation`); err == nil {
		em := map[string]bool{}
		for r.Next() {
			var id string
			r.Scan(&id)
			em[id] = true
		}
		r.Close()
		for i := range b.Persons {
			if em[b.Persons[i].ID] {
				b.Persons[i].Emancipated = true
			}
		}
	}

	// Driver licenses
	if r, err := db.QueryContext(ctx, `SELECT person_id, number FROM driver_license`); err == nil {
		for r.Next() {
			var d data.DriverLicense
			r.Scan(&d.PersonID, &d.Number)
			b.DriverLicenses = append(b.DriverLicenses, d)
		}
		r.Close()
	}

	return b, nil
}

// ---------- helpers ----------

func normalizeEdge(a, b, rel string) (string, string, string) {
	switch strings.ToLower(strings.TrimSpace(rel)) {
	case "брат", "сестра":
		return a, b, "sibling"
	case "супруг(а)", "супруг", "супруга":
		return a, b, "spouse"
	case "мать", "отец":
		return a, b, "parent"
	case "сын", "дочь":
		return b, a, "parent"
	}
	return "", "", ""
}

func parseAddress(addr string) (city, street, house string) {
	parts := strings.Split(addr, ", ")
	if len(parts) > 0 {
		city = parts[0]
	}
	if len(parts) > 1 {
		street = parts[1]
	}
	if len(parts) > 2 {
		house = parts[2]
	}
	return
}

func parseCitizenships(raw string) []string {
	if raw == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(raw, ",") {
		switch strings.TrimSpace(p) {
		case "РФ", "Россия", "Russia":
			out = append(out, "RF")
		case "Таджикистан", "Tajikistan":
			out = append(out, "TJ")
		case "Казахстан", "Kazakhstan":
			out = append(out, "KZ")
		}
	}
	return out
}
