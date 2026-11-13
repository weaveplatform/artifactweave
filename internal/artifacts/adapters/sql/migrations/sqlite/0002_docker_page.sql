-- The registry's tags lists and catalogs read their pages from these two
-- indexes over the stored Docker, OCI and Helm OCI manifests (#1402,
-- decision 0031), so a late page is a seek rather than a sort of every
-- manifest of the image or repository. Each expression must stay written
-- exactly as the queries in adapters/sql/docker_page.go write it, or the
-- planner no longer uses the index.
-- +up

-- A tags list's order: the folded path, then its bytes. Below one image the
-- folded path is the image, a slash and the folded tag. SQLite's lower()
-- folds A to Z only.
CREATE INDEX items_docker_tags ON items (repo_key, cached, lower(path), path)
    WHERE type = 'file' AND name IN ('manifest.json', 'list.manifest.json');

-- A catalog's order: the image, which is the path without its last segment
-- (rtrim strips the trailing characters that are not slashes), then the
-- path. Every function in it is deterministic, as an index expression must
-- be.
CREATE INDEX items_docker_images ON items (
    repo_key, cached, substr(path, 1, length(rtrim(path, replace(path, '/', ''))) - 1), path
) WHERE type = 'file' AND name IN ('manifest.json', 'list.manifest.json');

-- +down
DROP INDEX items_docker_images;
DROP INDEX items_docker_tags;
