-- migrations/0001_init.sql
-- Идемпотентная схема прототипа.

PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS schema_version (
    version INTEGER PRIMARY KEY
);

CREATE TABLE IF NOT EXISTS person (
    person_id TEXT PRIMARY KEY,
    role      TEXT NOT NULL CHECK (role IN ('PHYS','DLTO')),
    payload   TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS passport (
    person_id TEXT PRIMARY KEY REFERENCES person(person_id),
    number    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_passport_number ON passport(number);

CREATE TABLE IF NOT EXISTS kinship (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    a        TEXT NOT NULL REFERENCES person(person_id),
    b        TEXT NOT NULL REFERENCES person(person_id),
    kind     TEXT NOT NULL CHECK (kind IN ('parent','spouse','sibling')),
    civil_id TEXT
);

CREATE TABLE IF NOT EXISTS civil_registry (
    record_id TEXT PRIMARY KEY,
    a         TEXT NOT NULL REFERENCES person(person_id),
    b         TEXT NOT NULL REFERENCES person(person_id),
    kind      TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS divorce (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    a  TEXT NOT NULL REFERENCES person(person_id),
    b  TEXT NOT NULL REFERENCES person(person_id)
);

CREATE TABLE IF NOT EXISTS guardianship (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    guardian TEXT NOT NULL REFERENCES person(person_id),
    ward     TEXT NOT NULL REFERENCES person(person_id)
);

CREATE TABLE IF NOT EXISTS emancipation (
    person_id TEXT PRIMARY KEY REFERENCES person(person_id)
);

CREATE TABLE IF NOT EXISTS parental_rights (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    person_id TEXT NOT NULL REFERENCES person(person_id),
    child_id  TEXT REFERENCES person(person_id)
);

CREATE TABLE IF NOT EXISTS legal_capacity (
    person_id TEXT PRIMARY KEY REFERENCES person(person_id),
    state     TEXT NOT NULL CHECK (state IN ('full','limited','incapable'))
);

CREATE TABLE IF NOT EXISTS employment_service (
    person_id  TEXT PRIMARY KEY REFERENCES person(person_id),
    unemployed INTEGER NOT NULL CHECK (unemployed IN (0,1))
);

CREATE TABLE IF NOT EXISTS tax_registration (
    person_id   TEXT PRIMARY KEY REFERENCES person(person_id),
    ogrnip_json TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS driver_license (
    person_id TEXT PRIMARY KEY REFERENCES person(person_id),
    number    TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS ldt_order (
    order_id         TEXT PRIMARY KEY,
    created_at       TEXT NOT NULL,
    checked_at       TEXT,
    dlto_id          TEXT NOT NULL REFERENCES person(person_id),
    person_id        TEXT REFERENCES person(person_id),
    person_ref       TEXT NOT NULL,
    status           TEXT NOT NULL CHECK (status IN
                     ('draft','pending_review','approved','reassigned','cancelled')),
    signal_json      TEXT,
    rule_version     TEXT,
    check_token      TEXT,
    reviewer_id      TEXT,
    reviewer_comment TEXT,
    decided_at       TEXT
);
CREATE INDEX IF NOT EXISTS ix_ldt_order_status ON ldt_order(status);

CREATE TABLE IF NOT EXISTS audit_log (
    audit_id     INTEGER PRIMARY KEY AUTOINCREMENT,
    occurred_at  TEXT NOT NULL,
    order_id     TEXT NOT NULL,
    action       TEXT NOT NULL CHECK (action IN
                 ('check_started','check_completed','check_failed',
                  'approved','reassigned','cancelled')),
    actor_id     TEXT,
    dlto_id      TEXT,
    person_id    TEXT,
    rule_version TEXT,
    latency_ms   INTEGER,
    smev_status  TEXT,
    signal_json  TEXT,
    comment      TEXT
);
CREATE INDEX IF NOT EXISTS ix_audit_order ON audit_log(order_id, occurred_at);

CREATE TRIGGER IF NOT EXISTS audit_log_no_update
BEFORE UPDATE ON audit_log
BEGIN
    SELECT RAISE(ABORT, 'audit_log is append-only');
END;

CREATE TRIGGER IF NOT EXISTS audit_log_no_delete
BEFORE DELETE ON audit_log
BEGIN
    SELECT RAISE(ABORT, 'audit_log is append-only');
END;