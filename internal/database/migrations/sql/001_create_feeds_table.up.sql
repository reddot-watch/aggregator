CREATE TABLE IF NOT EXISTS feeds (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    url TEXT NOT NULL UNIQUE,
    comments TEXT,
    language TEXT,
    status TEXT DEFAULT 'active',
    failures_count INTEGER DEFAULT 0,
    last_error TEXT,
    last_retrieved_at DATETIME,
    created_at DATETIME NOT NULL,
    updated_at DATETIME NOT NULL,
    deleted_at DATETIME
); 