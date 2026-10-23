ALTER TABLE purchases ADD COLUMN source_evidence TEXT NOT NULL DEFAULT '';
ALTER TABLE purchases ADD COLUMN source_url TEXT;
ALTER TABLE purchases ADD COLUMN source_observed_at INTEGER;
ALTER TABLE purchases ADD COLUMN reconciliation_origin_id TEXT;

ALTER TABLE purchase_lines ADD COLUMN line_number INTEGER NOT NULL DEFAULT 0;
ALTER TABLE purchase_lines ADD COLUMN product_revision_snapshot INTEGER;
ALTER TABLE purchase_lines ADD COLUMN sources_snapshot_json TEXT NOT NULL DEFAULT '[]' CHECK (json_valid(sources_snapshot_json));
ALTER TABLE purchase_lines ADD COLUMN list_link_json TEXT CHECK (list_link_json IS NULL OR json_valid(list_link_json));

-- A receipt/confirmation identity belongs to one correction lineage. A void
-- retains that claim too; recording the same source again must be explicit.
CREATE TABLE purchase_sources (
    owner_id TEXT NOT NULL,
    retailer TEXT NOT NULL COLLATE NOCASE,
    source_kind TEXT NOT NULL,
    source_reference TEXT NOT NULL,
    purchase_id TEXT NOT NULL,
    PRIMARY KEY (owner_id, retailer, source_kind, source_reference),
    FOREIGN KEY (owner_id, purchase_id) REFERENCES purchases(owner_id, id)
) STRICT;

CREATE UNIQUE INDEX one_purchase_replacement_draft ON purchases(owner_id, replaces_id)
    WHERE replaces_id IS NOT NULL AND status = 'draft';

CREATE TABLE purchase_reconciliations (
    owner_id TEXT NOT NULL,
    purchase_id TEXT NOT NULL,
    list_id TEXT NOT NULL,
    item_id TEXT NOT NULL,
    purchased_quantity TEXT NOT NULL,
    before_json TEXT NOT NULL CHECK (json_valid(before_json)),
    after_json TEXT NOT NULL CHECK (json_valid(after_json)),
    PRIMARY KEY (owner_id, purchase_id, item_id),
    FOREIGN KEY (owner_id, purchase_id) REFERENCES purchases(owner_id, id),
    FOREIGN KEY (owner_id, list_id) REFERENCES shopping_lists(owner_id, id),
    FOREIGN KEY (owner_id, item_id) REFERENCES list_items(owner_id, id)
) STRICT;

-- Old foundation fixtures remain readable; runtime purchase workflows require
-- complete evidence before confirmation. Never backfill invented evidence.
INSERT OR IGNORE INTO purchase_sources(owner_id,retailer,source_kind,source_reference,purchase_id)
    SELECT owner_id,retailer,source_kind,source_reference,id FROM purchases
    WHERE source_reference IS NOT NULL AND replaces_id IS NULL;

CREATE TRIGGER purchase_header_immutable_after_confirmation
BEFORE UPDATE OF retailer, store_location, purchased_at, currency, source_kind,
    source_reference, source_evidence, source_url, source_observed_at, replaces_id,
    reconciliation_origin_id ON purchases
WHEN OLD.status <> 'draft'
BEGIN SELECT RAISE(ABORT, 'purchase snapshot is immutable'); END;

CREATE TRIGGER purchase_reconciliation_no_update
BEFORE UPDATE ON purchase_reconciliations
BEGIN SELECT RAISE(ABORT, 'purchase reconciliation is immutable'); END;

CREATE TRIGGER purchase_reconciliation_no_delete
BEFORE DELETE ON purchase_reconciliations
BEGIN SELECT RAISE(ABORT, 'purchase reconciliation is immutable'); END;

CREATE TRIGGER purchase_reconciliation_no_insert_after_confirmation
BEFORE INSERT ON purchase_reconciliations
WHEN (SELECT status FROM purchases WHERE owner_id=NEW.owner_id AND id=NEW.purchase_id) <> 'draft'
BEGIN SELECT RAISE(ABORT, 'purchase reconciliation is immutable'); END;
