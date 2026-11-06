-- Initial schema for home-streaming-assistant. All timestamps are UTC
-- RFC3339Nano strings; schedule time is HH:MM and timezone an IANA name.
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS download_jobs (
    id               TEXT PRIMARY KEY,
    source           TEXT NOT NULL CHECK (source IN ('aliyun','gdrive')),
    path             TEXT NOT NULL,
    version          TEXT NOT NULL,
    origin           TEXT NOT NULL CHECK (origin IN ('manual','schedule')),
    state            TEXT NOT NULL CHECK (state IN ('queued','downloading','verifying','publishing','completed','cancelled','failed')),
    attempt          INTEGER NOT NULL DEFAULT 1,
    remote_json      TEXT NOT NULL DEFAULT '',
    bytes_done       INTEGER NOT NULL DEFAULT 0,
    bytes_total      INTEGER NOT NULL DEFAULT 0,
    cancel_requested INTEGER NOT NULL DEFAULT 0,
    final_rel        TEXT,
    local_sha256     TEXT,
    error_code       TEXT,
    created_at       TEXT NOT NULL,
    updated_at       TEXT NOT NULL,
    UNIQUE (source, path, version)
);

CREATE INDEX IF NOT EXISTS idx_jobs_state_created
    ON download_jobs (state, created_at DESC, id DESC);

CREATE TABLE IF NOT EXISTS library_files (
    id                  TEXT PRIMARY KEY,
    job_id              TEXT NOT NULL REFERENCES download_jobs(id),
    attempt             INTEGER NOT NULL,
    source              TEXT NOT NULL CHECK (source IN ('aliyun','gdrive')),
    path                TEXT NOT NULL,
    version             TEXT NOT NULL,
    relative_path       TEXT NOT NULL UNIQUE,
    size                INTEGER NOT NULL,
    local_sha256        TEXT NOT NULL,
    state               TEXT NOT NULL CHECK (state IN ('pending_index','playable','deleting','deleted','missing')),
    error_code          TEXT,
    jellyfin_item_id    TEXT,
    jellyfin_server_id  TEXT,
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL,
    UNIQUE (job_id, attempt)
);

CREATE INDEX IF NOT EXISTS idx_library_state ON library_files (state);

CREATE TABLE IF NOT EXISTS gdrive_schedule (
    id               INTEGER PRIMARY KEY CHECK (id = 1),
    path             TEXT NOT NULL DEFAULT '',
    enabled          INTEGER NOT NULL DEFAULT 0,
    time             TEXT NOT NULL DEFAULT '03:00',
    timezone         TEXT NOT NULL DEFAULT 'Asia/Shanghai',
    revision         INTEGER NOT NULL DEFAULT 0,
    last_slot        TEXT,
    last_started_at  TEXT,
    last_finished_at TEXT,
    last_status      TEXT,
    last_error_code  TEXT,
    last_enqueued    INTEGER
);

INSERT INTO gdrive_schedule (id) VALUES (1) ON CONFLICT (id) DO NOTHING;

CREATE TABLE IF NOT EXISTS sessions (
    token_hash TEXT PRIMARY KEY,
    csrf_token TEXT NOT NULL,
    expires_at TEXT NOT NULL
);
