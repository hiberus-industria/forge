CREATE TABLE kv (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- Harness session → working directory. Local only, never synced.
CREATE TABLE sessions (
    harness    TEXT    NOT NULL,
    session_id TEXT    NOT NULL,
    cwd        TEXT    NOT NULL,
    started_at INTEGER NOT NULL,
    PRIMARY KEY (harness, session_id)
);

CREATE TABLE events (
    dedup_key     TEXT PRIMARY KEY,
    harness       TEXT    NOT NULL,
    external_id   TEXT    NOT NULL,
    session_id    TEXT    NOT NULL,
    provider      TEXT    NOT NULL DEFAULT '',
    model         TEXT    NOT NULL,
    input         INTEGER NOT NULL DEFAULT 0,
    output        INTEGER NOT NULL DEFAULT 0,
    reasoning     INTEGER NOT NULL DEFAULT 0,
    cache_read    INTEGER NOT NULL DEFAULT 0,
    cache_write   INTEGER NOT NULL DEFAULT 0,
    occurred_at   INTEGER NOT NULL, -- unix ms
    reported_cost INTEGER NOT NULL DEFAULT 0, -- USD micros
    estimate      INTEGER NOT NULL DEFAULT 0, -- USD micros, local price book
    machine_id    TEXT    NOT NULL,
    attribution   TEXT    NOT NULL, -- JSON domain.Attribution
    status        TEXT    NOT NULL DEFAULT 'pending',
    reject_reason TEXT,
    created_at    INTEGER NOT NULL,
    synced_at     INTEGER
);

CREATE INDEX events_status_occurred ON events (status, occurred_at);
CREATE INDEX events_occurred ON events (occurred_at);
