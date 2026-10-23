CREATE TABLE products (
    owner_id TEXT NOT NULL REFERENCES accounts(id),
    id TEXT NOT NULL,
    source_key TEXT,
    name TEXT NOT NULL CHECK (length(name) > 0),
    brand TEXT NOT NULL DEFAULT '',
    flavor TEXT NOT NULL DEFAULT '',
    category TEXT NOT NULL CHECK (category IN ('produce', 'meat', 'drinks', 'pantry', 'dairy', 'other')),
    package_count INTEGER CHECK (package_count > 0),
    package_size TEXT,
    package_unit TEXT,
    description TEXT NOT NULL DEFAULT '',
    archived INTEGER NOT NULL DEFAULT 0 CHECK (archived IN (0, 1)),
    revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (owner_id, id),
    UNIQUE (owner_id, source_key),
    CHECK ((package_size IS NULL) = (package_unit IS NULL))
) STRICT;

CREATE TABLE product_media (
    owner_id TEXT NOT NULL,
    id TEXT NOT NULL,
    product_id TEXT NOT NULL,
    image_url TEXT NOT NULL,
    source_url TEXT NOT NULL,
    observed_at INTEGER NOT NULL,
    PRIMARY KEY (owner_id, id),
    FOREIGN KEY (owner_id, product_id) REFERENCES products(owner_id, id)
) STRICT;

CREATE TABLE product_nutrition (
    owner_id TEXT NOT NULL,
    product_id TEXT NOT NULL,
    serving_size TEXT NOT NULL,
    serving_unit TEXT NOT NULL,
    facts_json TEXT NOT NULL CHECK (json_valid(facts_json)),
    source_url TEXT NOT NULL,
    observed_at INTEGER NOT NULL,
    verification TEXT NOT NULL CHECK (verification IN ('unverified', 'verified')),
    PRIMARY KEY (owner_id, product_id),
    FOREIGN KEY (owner_id, product_id) REFERENCES products(owner_id, id)
) STRICT;

CREATE TABLE retailer_listings (
    owner_id TEXT NOT NULL,
    id TEXT NOT NULL,
    product_id TEXT NOT NULL,
    retailer TEXT NOT NULL,
    retailer_item_id TEXT,
    product_url TEXT NOT NULL,
    store_location TEXT,
    price_minor INTEGER CHECK (price_minor >= 0),
    currency TEXT CHECK (length(currency) = 3 AND currency NOT GLOB '*[^A-Z]*'),
    observed_at INTEGER,
    availability TEXT NOT NULL DEFAULT 'unknown' CHECK (availability IN ('unknown', 'in_stock', 'out_of_stock')),
    PRIMARY KEY (owner_id, id),
    FOREIGN KEY (owner_id, product_id) REFERENCES products(owner_id, id),
    CHECK (price_minor IS NULL OR (currency IS NOT NULL AND observed_at IS NOT NULL)),
    CHECK (availability = 'unknown' OR observed_at IS NOT NULL)
) STRICT;

CREATE TABLE shopping_lists (
    owner_id TEXT NOT NULL REFERENCES accounts(id),
    id TEXT NOT NULL,
    title TEXT NOT NULL CHECK (length(title) > 0),
    retailer TEXT,
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
    revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (owner_id, id)
) STRICT;

CREATE TABLE list_items (
    owner_id TEXT NOT NULL,
    id TEXT NOT NULL,
    list_id TEXT NOT NULL,
    product_id TEXT NOT NULL,
    quantity TEXT NOT NULL,
    picked_up INTEGER NOT NULL DEFAULT 0 CHECK (picked_up IN (0, 1)),
    removed_at INTEGER,
    revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    created_by TEXT NOT NULL,
    updated_by TEXT NOT NULL,
    PRIMARY KEY (owner_id, id),
    FOREIGN KEY (owner_id, list_id) REFERENCES shopping_lists(owner_id, id),
    FOREIGN KEY (owner_id, product_id) REFERENCES products(owner_id, id)
) STRICT;

CREATE INDEX list_items_by_list ON list_items(owner_id, list_id, removed_at);

CREATE TABLE purchases (
    owner_id TEXT NOT NULL REFERENCES accounts(id),
    id TEXT NOT NULL,
    retailer TEXT NOT NULL,
    store_location TEXT,
    purchased_at INTEGER,
    currency TEXT CHECK (length(currency) = 3 AND currency NOT GLOB '*[^A-Z]*'),
    status TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'confirmed', 'void', 'superseded')),
    source_reference TEXT,
    source_kind TEXT NOT NULL CHECK (source_kind IN ('receipt', 'user_confirmation')),
    replaces_id TEXT,
    superseded_by_id TEXT,
    void_reason TEXT,
    revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (owner_id, id),
    FOREIGN KEY (owner_id, replaces_id) REFERENCES purchases(owner_id, id),
    FOREIGN KEY (owner_id, superseded_by_id) REFERENCES purchases(owner_id, id),
    CHECK (status = 'draft' OR (purchased_at IS NOT NULL AND currency IS NOT NULL)),
    CHECK (status <> 'void' OR (void_reason IS NOT NULL AND length(void_reason) > 0))
) STRICT;

CREATE INDEX purchases_by_date ON purchases(owner_id, status, purchased_at, id);

CREATE TABLE purchase_lines (
    owner_id TEXT NOT NULL,
    id TEXT NOT NULL,
    purchase_id TEXT NOT NULL,
    product_id TEXT,
    source_list_item_id TEXT,
    name_snapshot TEXT NOT NULL,
    brand_snapshot TEXT NOT NULL DEFAULT '',
    flavor_snapshot TEXT NOT NULL DEFAULT '',
    category_snapshot TEXT NOT NULL CHECK (category_snapshot IN ('produce', 'meat', 'drinks', 'pantry', 'dairy', 'other')),
    package_count_snapshot INTEGER CHECK (package_count_snapshot > 0),
    package_size_snapshot TEXT,
    package_unit_snapshot TEXT,
    quantity TEXT NOT NULL,
    quantity_unit TEXT NOT NULL,
    unit_price_minor INTEGER CHECK (unit_price_minor >= 0),
    line_total_minor INTEGER CHECK (line_total_minor >= 0),
    discount_minor INTEGER CHECK (discount_minor >= 0),
    tax_minor INTEGER CHECK (tax_minor >= 0),
    tax_treatment TEXT NOT NULL DEFAULT 'unknown' CHECK (tax_treatment IN ('unknown', 'included', 'excluded')),
    source_reference TEXT,
    PRIMARY KEY (owner_id, id),
    FOREIGN KEY (owner_id, purchase_id) REFERENCES purchases(owner_id, id),
    FOREIGN KEY (owner_id, product_id) REFERENCES products(owner_id, id),
    FOREIGN KEY (owner_id, source_list_item_id) REFERENCES list_items(owner_id, id),
    CHECK ((package_size_snapshot IS NULL) = (package_unit_snapshot IS NULL))
) STRICT;

CREATE INDEX purchase_lines_by_purchase ON purchase_lines(owner_id, purchase_id);

-- A confirmed snapshot remains immutable after voiding or replacement too.
CREATE TRIGGER purchase_lines_no_insert_after_confirmation
BEFORE INSERT ON purchase_lines
WHEN (SELECT status FROM purchases WHERE owner_id = NEW.owner_id AND id = NEW.purchase_id) <> 'draft'
BEGIN SELECT RAISE(ABORT, 'purchase snapshot is immutable'); END;

CREATE TRIGGER purchase_lines_no_update_after_confirmation
BEFORE UPDATE ON purchase_lines
WHEN (SELECT status FROM purchases WHERE owner_id = OLD.owner_id AND id = OLD.purchase_id) <> 'draft'
  OR (SELECT status FROM purchases WHERE owner_id = NEW.owner_id AND id = NEW.purchase_id) <> 'draft'
BEGIN SELECT RAISE(ABORT, 'purchase snapshot is immutable'); END;

CREATE TRIGGER purchase_lines_no_delete_after_confirmation
BEFORE DELETE ON purchase_lines
WHEN (SELECT status FROM purchases WHERE owner_id = OLD.owner_id AND id = OLD.purchase_id) <> 'draft'
BEGIN SELECT RAISE(ABORT, 'purchase snapshot is immutable'); END;

CREATE TRIGGER purchases_no_return_to_draft
BEFORE UPDATE OF status ON purchases
WHEN OLD.status <> 'draft' AND NEW.status = 'draft'
BEGIN SELECT RAISE(ABORT, 'confirmed purchase cannot return to draft'); END;

CREATE TRIGGER purchases_no_delete_after_confirmation
BEFORE DELETE ON purchases
WHEN OLD.status <> 'draft'
BEGIN SELECT RAISE(ABORT, 'confirmed purchase is retained'); END;
