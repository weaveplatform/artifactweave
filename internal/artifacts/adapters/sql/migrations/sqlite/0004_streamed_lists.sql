-- Two complete lists read their pages from these indexes (#1409, decision
-- 0043, "Streaming complete lists"), so each page is a seek rather than a
-- sort of the whole list. Each expression must stay written exactly as the
-- queries in adapters/sql/streamed_lists.go write it, or the planner no
-- longer uses the index.
-- +up

-- The storage API's file list sorts its entries by full path byte by byte:
-- a root-level item's name, or its path, a slash and its name. SQLite's
-- BINARY collation compares text byte by byte, as Go's strings.Compare
-- does.
CREATE INDEX items_full_path ON items (
    repo_key, cached, CASE WHEN path = '.' THEN name ELSE path || '/' || name END
);

-- The dependency search reads a checksum's dependencies in run, module and
-- file order. The leading sha1 serves every lookup the single-column index
-- it replaces served.
DROP INDEX build_dependencies_sha1;
CREATE INDEX build_dependencies_sha1_order ON build_dependencies (
    sha1, build_id, module_position, position
);

-- +down
DROP INDEX build_dependencies_sha1_order;
CREATE INDEX build_dependencies_sha1 ON build_dependencies (sha1);
DROP INDEX items_full_path;
