CREATE TABLE IF NOT EXISTS library_cache (
  id          INTEGER PRIMARY KEY,
  library_key TEXT NOT NULL,
  query       TEXT NOT NULL,
  result_json TEXT NOT NULL,
  fetched_at  INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_lib_cache ON library_cache(library_key, query);

CREATE TABLE IF NOT EXISTS amazon_cache (
  id          INTEGER PRIMARY KEY,
  isbn        TEXT NOT NULL UNIQUE,
  result_json TEXT NOT NULL,
  fetched_at  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS libraries (
  key     TEXT PRIMARY KEY,
  name    TEXT NOT NULL,
  website TEXT
);

-- Full shelf search result cache (TTL 5 min by default)
CREATE TABLE IF NOT EXISTS shelf_cache (
  id          INTEGER PRIMARY KEY,
  cache_key   TEXT NOT NULL UNIQUE,
  events_json TEXT NOT NULL,
  fetched_at  INTEGER NOT NULL
);

-- Shortlinks: shareable tokens that encode a search URL + library set
CREATE TABLE IF NOT EXISTS shortlinks (
  id         INTEGER PRIMARY KEY,
  token      TEXT NOT NULL UNIQUE,
  url        TEXT NOT NULL,
  libraries  TEXT NOT NULL,  -- comma-separated library keys
  created_at INTEGER NOT NULL DEFAULT (unixepoch())
);
