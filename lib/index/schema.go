package index

// schema is idempotent. notes holds per-file metadata for reconciliation and the title;
// chunks holds bounded pieces of note content. Both FTS5 tables are external-content
// tables kept in sync by triggers, so content is stored only once. attachments holds
// filenames only; attachment contents are never indexed.
const schema = `
CREATE TABLE IF NOT EXISTS notes (
	path     TEXT PRIMARY KEY,
	title    TEXT NOT NULL,
	revision TEXT NOT NULL,
	size     INTEGER NOT NULL,
	mtime_ns INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS chunks (
	id      INTEGER PRIMARY KEY,
	path    TEXT NOT NULL REFERENCES notes(path) ON DELETE CASCADE,
	seq     INTEGER NOT NULL,
	content TEXT NOT NULL,
	UNIQUE (path, seq)
);

CREATE TABLE IF NOT EXISTS attachments (
	path     TEXT PRIMARY KEY,
	name     TEXT NOT NULL,
	size     INTEGER NOT NULL,
	mtime_ns INTEGER NOT NULL
);

CREATE VIRTUAL TABLE IF NOT EXISTS notes_fts USING fts5(
	title, content='notes', content_rowid='rowid'
);

CREATE VIRTUAL TABLE IF NOT EXISTS chunks_fts USING fts5(
	content, content='chunks', content_rowid='id'
);

CREATE TRIGGER IF NOT EXISTS notes_ai AFTER INSERT ON notes BEGIN
	INSERT INTO notes_fts(rowid, title) VALUES (new.rowid, new.title);
END;
CREATE TRIGGER IF NOT EXISTS notes_ad AFTER DELETE ON notes BEGIN
	INSERT INTO notes_fts(notes_fts, rowid, title) VALUES ('delete', old.rowid, old.title);
END;
CREATE TRIGGER IF NOT EXISTS notes_au AFTER UPDATE ON notes BEGIN
	INSERT INTO notes_fts(notes_fts, rowid, title) VALUES ('delete', old.rowid, old.title);
	INSERT INTO notes_fts(rowid, title) VALUES (new.rowid, new.title);
END;

CREATE VIRTUAL TABLE IF NOT EXISTS attachments_fts USING fts5(
	name, content='attachments', content_rowid='rowid'
);

CREATE TRIGGER IF NOT EXISTS attachments_ai AFTER INSERT ON attachments BEGIN
	INSERT INTO attachments_fts(rowid, name) VALUES (new.rowid, new.name);
END;
CREATE TRIGGER IF NOT EXISTS attachments_ad AFTER DELETE ON attachments BEGIN
	INSERT INTO attachments_fts(attachments_fts, rowid, name) VALUES ('delete', old.rowid, old.name);
END;
CREATE TRIGGER IF NOT EXISTS attachments_au AFTER UPDATE ON attachments BEGIN
	INSERT INTO attachments_fts(attachments_fts, rowid, name) VALUES ('delete', old.rowid, old.name);
	INSERT INTO attachments_fts(rowid, name) VALUES (new.rowid, new.name);
END;

CREATE TRIGGER IF NOT EXISTS chunks_ai AFTER INSERT ON chunks BEGIN
	INSERT INTO chunks_fts(rowid, content) VALUES (new.id, new.content);
END;
CREATE TRIGGER IF NOT EXISTS chunks_ad AFTER DELETE ON chunks BEGIN
	INSERT INTO chunks_fts(chunks_fts, rowid, content) VALUES ('delete', old.id, old.content);
END;
CREATE TRIGGER IF NOT EXISTS chunks_au AFTER UPDATE ON chunks BEGIN
	INSERT INTO chunks_fts(chunks_fts, rowid, content) VALUES ('delete', old.id, old.content);
	INSERT INTO chunks_fts(rowid, content) VALUES (new.id, new.content);
END;
`
