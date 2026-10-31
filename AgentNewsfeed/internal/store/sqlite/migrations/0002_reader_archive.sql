ALTER TABLE articles ADD COLUMN content_markdown TEXT NOT NULL DEFAULT ''
    CHECK (length(CAST(content_markdown AS BLOB)) <= 262144);
ALTER TABLE articles ADD COLUMN archived_at INTEGER CHECK (archived_at IS NULL OR archived_at > 0);
CREATE INDEX articles_active_by_owner_time ON articles(owner_id, created_at DESC, id DESC) WHERE archived_at IS NULL;
CREATE INDEX articles_archived_by_owner_time ON articles(owner_id, archived_at DESC, id DESC) WHERE archived_at IS NOT NULL;
