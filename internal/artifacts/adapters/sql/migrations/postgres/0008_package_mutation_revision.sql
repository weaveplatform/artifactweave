-- Version identities already separate existing rows; every subsequent owned
-- mutation replaces this initial marker in the same metadata transaction.
-- +up
ALTER TABLE package_versions ADD COLUMN mutation_revision TEXT COLLATE "C" NOT NULL DEFAULT 'initial' CHECK (mutation_revision <> '');
-- +down
ALTER TABLE package_versions DROP COLUMN mutation_revision;
