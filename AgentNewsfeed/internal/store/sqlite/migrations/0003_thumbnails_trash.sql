ALTER TABLE articles ADD COLUMN trashed_at INTEGER CHECK (trashed_at IS NULL OR trashed_at > 0);
ALTER TABLE articles ADD COLUMN thumbnail_status TEXT NOT NULL DEFAULT ''
    CHECK (thumbnail_status IN ('', 'legacy', 'stored', 'unavailable', 'budget_exceeded'));
UPDATE articles SET thumbnail_status = 'legacy' WHERE thumbnail_url != '';

CREATE TABLE thumbnail_blobs (
    owner_id TEXT NOT NULL REFERENCES accounts(id),
    hash TEXT NOT NULL CHECK (length(hash) = 64),
    jpeg BLOB NOT NULL CHECK (length(jpeg) BETWEEN 1 AND 102400),
    width INTEGER NOT NULL CHECK (width BETWEEN 1 AND 640),
    height INTEGER NOT NULL CHECK (height BETWEEN 1 AND 640),
    PRIMARY KEY (owner_id, hash)
) STRICT;

CREATE TABLE article_thumbnails (
    owner_id TEXT NOT NULL,
    article_id TEXT NOT NULL,
    hash TEXT NOT NULL,
    PRIMARY KEY (owner_id, article_id),
    FOREIGN KEY (owner_id, article_id) REFERENCES articles(owner_id, id),
    FOREIGN KEY (owner_id, hash) REFERENCES thumbnail_blobs(owner_id, hash)
) STRICT;
CREATE INDEX article_thumbnails_by_hash ON article_thumbnails(owner_id, hash);
CREATE INDEX articles_trash_by_owner_time ON articles(owner_id, trashed_at DESC, id DESC) WHERE trashed_at IS NOT NULL;
