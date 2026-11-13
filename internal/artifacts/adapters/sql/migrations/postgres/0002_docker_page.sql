-- The registry's tags lists and catalogs read their pages from these two
-- indexes over the stored Docker, OCI and Helm OCI manifests (#1402,
-- decision 0031), so a late page is a seek rather than a sort of every
-- manifest of the image or repository. Each expression must stay written
-- exactly as the queries in adapters/sql/docker_page.go write it, or the
-- planner no longer uses the index.
-- +up

-- A tags list's order: the folded path, then its bytes. Below one image the
-- folded path is the image, a slash and the folded tag. Under COLLATE "C"
-- lower() folds A to Z only, as SQLite's does, and both columns order byte
-- by byte.
CREATE INDEX items_docker_tags ON items (
    repo_key, cached, (lower(path) COLLATE "C"), path COLLATE "C"
) WHERE type = 'file' AND name IN ('manifest.json', 'list.manifest.json');

-- A catalog's order: the image, which is the path without its last segment,
-- then the path. left, length, strpos and reverse are all IMMUTABLE, as an
-- index expression must be.
CREATE INDEX items_docker_images ON items (
    repo_key, cached, (left(path, length(path) - strpos(reverse(path), '/')) COLLATE "C"),
    path COLLATE "C"
) WHERE type = 'file' AND name IN ('manifest.json', 'list.manifest.json');

-- +down
DROP INDEX items_docker_images;
DROP INDEX items_docker_tags;
