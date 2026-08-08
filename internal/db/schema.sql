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

-- Parsed Goodreads shelf cache: []models.Book JSON keyed "userID|shelf"
-- (TTL 5 min by default; skips RSS refetches on repeat searches)
CREATE TABLE IF NOT EXISTS shelf_cache (
  id          INTEGER PRIMARY KEY,
  cache_key   TEXT NOT NULL UNIQUE,
  events_json TEXT NOT NULL,
  fetched_at  INTEGER NOT NULL
);

-- Project Gutenberg catalog (refreshed weekly on startup)
CREATE TABLE IF NOT EXISTS gutenberg_books (
  id        INTEGER PRIMARY KEY,  -- Gutenberg text number
  title     TEXT NOT NULL,
  author    TEXT NOT NULL,        -- normalized: "lastname firstname" lowercase
  epub_url  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_gutenberg_title ON gutenberg_books(title);

-- Per-book result cache: full BookEvent keyed by goodreads_id + sorted libraries (TTL 2h default)
CREATE TABLE IF NOT EXISTS book_cache (
  id           INTEGER PRIMARY KEY,
  goodreads_id TEXT NOT NULL,
  libraries    TEXT NOT NULL,  -- sorted comma-separated library keys
  event_json   TEXT NOT NULL,
  fetched_at   INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_book_cache ON book_cache(goodreads_id, libraries);

-- Seed: well-known libraries so autocomplete works on a fresh install.
-- INSERT OR IGNORE so re-running migrations is safe.
INSERT OR IGNORE INTO libraries (key, name, website) VALUES
  ('nypl',      'New York Public Library',          'https://www.nypl.org/'),
  ('bklynlib',  'Brooklyn Public Library',          'https://www.bklynlibrary.org/'),
  ('qpl',       'Queens Public Library',            'https://www.queenslibrary.org/'),
  ('lapl',      'Los Angeles Public Library',       'https://www.lapl.org/'),
  ('sfpl',      'San Francisco Public Library',     'https://sfpl.org/'),
  ('spl',       'Seattle Public Library',           'https://www.spl.org/'),
  ('kcls',      'King County Library System',       'https://kcls.org/'),
  ('chipublib', 'Chicago Public Library',           'https://www.chipublib.org/'),
  ('multcolib', 'Multnomah County Library',         'https://multcolib.org/'),
  ('dcpl',      'DC Public Library',                'https://dclibrary.org/'),
  ('clevnet',   'CLEVNET',                          'https://www.cpl.org/'),
  ('hcpl',      'Harris County Public Library',     'https://hcpl.net/'),
  ('tpl',       'Toronto Public Library',           'https://www.torontopubliclibrary.ca/'),
  ('vpl',       'Vancouver Public Library',         'https://www.vpl.ca/'),
  ('freelibrary','Free Library of Philadelphia',    'https://www.freelibrary.org/');

-- Cached recommendation taste profile per Goodreads user: services.RecProfile
-- JSON (author weights, series progress, exclusion set). TTL 24h by default.
CREATE TABLE IF NOT EXISTS rec_profile (
  user_id      TEXT PRIMARY KEY,
  profile_json TEXT NOT NULL,
  fetched_at   INTEGER NOT NULL
);

-- Shortlinks: shareable tokens that encode a search URL + library set
CREATE TABLE IF NOT EXISTS shortlinks (
  id         INTEGER PRIMARY KEY,
  token      TEXT NOT NULL UNIQUE,
  url        TEXT NOT NULL,
  libraries  TEXT NOT NULL,  -- comma-separated library keys
  created_at INTEGER NOT NULL DEFAULT (unixepoch())
);

-- Recent searches: shelf + library sets to keep pre-warmed by the background
-- prewarm scheduler. One row per unique (user, shelf, library set); touched on
-- every successful search.
CREATE TABLE IF NOT EXISTS recent_searches (
  id                INTEGER PRIMARY KEY,
  goodreads_user_id TEXT NOT NULL,
  shelf             TEXT NOT NULL,
  libraries         TEXT NOT NULL,  -- sorted comma-separated library keys
  last_used_at      INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_recent_searches
  ON recent_searches(goodreads_user_id, shelf, libraries);
