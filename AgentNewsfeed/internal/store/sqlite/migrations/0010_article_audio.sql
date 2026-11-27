-- Additive: spoken audio the owner created for an article. While a row exists
-- the article's text is locked. The MP3 clips live in files named by the audio
-- ID; these tables hold what to say, progress and timing.
CREATE TABLE article_audio (
    owner_id TEXT NOT NULL,
    article_id TEXT NOT NULL,
    id TEXT NOT NULL UNIQUE,
    voice TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('generating', 'ready', 'failed')),
    error TEXT NOT NULL DEFAULT '',
    characters INTEGER NOT NULL CHECK (characters > 0),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (owner_id, article_id),
    FOREIGN KEY (owner_id, article_id) REFERENCES articles(owner_id, id)
) STRICT;
CREATE INDEX article_audio_by_status ON article_audio(status);

-- One spoken piece of the article, in reading order. block is the reader's
-- block index the app sent (-1 for the title and summary).
CREATE TABLE article_audio_segments (
    audio_id TEXT NOT NULL REFERENCES article_audio(id) ON DELETE CASCADE,
    idx INTEGER NOT NULL CHECK (idx >= 0),
    block INTEGER NOT NULL CHECK (block >= -1),
    text TEXT NOT NULL,
    done INTEGER NOT NULL DEFAULT 0 CHECK (done IN (0, 1)),
    duration_ms INTEGER NOT NULL DEFAULT 0,
    -- JSON array: start time in milliseconds of each Unicode code point of text.
    char_starts_ms TEXT NOT NULL DEFAULT '[]',
    PRIMARY KEY (audio_id, idx)
) STRICT;
