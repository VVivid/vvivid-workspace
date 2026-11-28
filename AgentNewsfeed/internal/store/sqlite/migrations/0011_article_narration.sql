-- Additive: an article's narration script, as JSON, or '' for none. Articles
-- saved before scripts existed keep '' and are read aloud as written.
ALTER TABLE articles ADD COLUMN narration TEXT NOT NULL DEFAULT '';
