-- Whether a user may download the original files. Administrators keep the
-- downloads they had; everyone else needs the right granted.
ALTER TABLE users ADD COLUMN can_download INTEGER NOT NULL DEFAULT 0;
UPDATE users SET can_download = 1 WHERE is_admin = 1;
