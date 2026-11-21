package sql

// A scalar projection lets the database sort by distinct acknowledged content
// before LIMIT. It does not hydrate report rows in the application. NULL makes
// an inconsistent content group fail scanning instead of publishing a size.
func (c cleanupStore) runContentExpression() string {
	size := "CAST(" + c.outcomeField("Removed", "Size") + " AS BIGINT)"
	hash := c.outcomeField("Removed", "SHA256")
	groups := `SELECT MIN(` + size + `) AS lo, MAX(` + size + `) AS hi
FROM cleanup_entries o WHERE o.run_id = r.id AND o.kind = 'item' AND o.state = 'deleted'
GROUP BY CASE WHEN COALESCE(` + hash + `, '') = '' THEN 'item:' || CAST(o.entity_id AS TEXT) ELSE 'hash:' || ` + hash + ` END`
	return `(SELECT CASE WHEN COUNT(*) <> COUNT(lo) OR MIN(lo) < 0 OR SUM(CASE WHEN lo <> hi THEN 1 ELSE 0 END) > 0
THEN NULL ELSE COALESCE(SUM(hi), 0) END FROM (` + groups + `) content)`
}
