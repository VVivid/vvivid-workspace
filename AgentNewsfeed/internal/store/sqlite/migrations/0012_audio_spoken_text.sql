-- Additive: what the voice says for a segment when the app sends a narration
-- script ('' means it reads the shown text), and the JSON sync points that map
-- the spoken text onto the shown text ('[]' for none).
ALTER TABLE article_audio_segments ADD COLUMN spoken TEXT NOT NULL DEFAULT '';
ALTER TABLE article_audio_segments ADD COLUMN sync TEXT NOT NULL DEFAULT '[]';
