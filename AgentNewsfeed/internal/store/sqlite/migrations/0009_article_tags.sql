-- Additive: existing articles, their order and durable retry bytes stay unchanged.
-- Tags are lowercase slugs: letters and digits joined by single hyphens.
CREATE TABLE article_tags (
    owner_id TEXT NOT NULL,
    article_id TEXT NOT NULL,
    tag TEXT NOT NULL CHECK (
        length(tag) BETWEEN 1 AND 32
        AND tag NOT GLOB '*[^a-z0-9-]*'
        AND tag NOT GLOB '-*' AND tag NOT GLOB '*-' AND tag NOT GLOB '*--*'
    ),
    PRIMARY KEY (owner_id, article_id, tag),
    FOREIGN KEY (owner_id, article_id) REFERENCES articles(owner_id, id)
) STRICT;
CREATE INDEX article_tags_by_owner_tag ON article_tags(owner_id, tag, article_id);
