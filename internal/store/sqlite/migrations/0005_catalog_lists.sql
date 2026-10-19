ALTER TABLE products ADD COLUMN sources_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(sources_json));
ALTER TABLE list_items ADD COLUMN retailer TEXT;
ALTER TABLE audit_events ADD COLUMN summary_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(summary_json));

-- Selection is explicit, independent of creation time or a list's title.
CREATE TABLE active_lists (
    owner_id TEXT PRIMARY KEY NOT NULL REFERENCES accounts(id),
    list_id TEXT,
    revision INTEGER NOT NULL CHECK (revision > 0),
    FOREIGN KEY (owner_id, list_id) REFERENCES shopping_lists(owner_id, id)
) STRICT;
