package db

// intentRouteSQL is shared by dispatch and route aggregation. Queries provide
// n (the intent node) and p (its earliest derived_from parent). Invalid legacy
// asset values fall through instead of failing the whole projection.
const intentRouteSQL = `COALESCE(
  NULLIF(btrim(n.payload->'contract'->>'route', E' \t\n\r'), ''),
  NULLIF(btrim(n.payload->>'risk_area', E' \t\n\r'), ''),
  NULLIF(btrim(n.payload->>'route', E' \t\n\r'), ''),
  CASE WHEN jsonb_typeof(n.payload->'asset_ids')='array' THEN
    CASE WHEN btrim(n.payload->'asset_ids'->>0) ~ '^\+?[0-9]{1,19}$' THEN
      CASE WHEN btrim(n.payload->'asset_ids'->>0)::numeric BETWEEN 1 AND 9223372036854775807
        THEN 'asset:' || (btrim(n.payload->'asset_ids'->>0)::bigint)::text
      END
    END
  END,
  'parent:' || p.parent_id::text,
  'unrouted'
)`

// IntentRoutes resolves route keys for a bounded set of intents. The parent
// aggregate only scans those ids, avoiding a full graph scan on each dispatch.
func (s *ExplorationStore) IntentRoutes(ids []int64) (map[int64]string, error) {
	out := map[int64]string{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(`WITH parents AS (
  SELECT dst_id, MIN(src_id) AS parent_id FROM exploration_edges
  WHERE exploration_id=$1 AND rel='derived_from' AND dst_id=ANY($2::bigint[])
  GROUP BY dst_id
)
SELECT n.id, `+intentRouteSQL+`
FROM exploration_nodes n LEFT JOIN parents p ON p.dst_id=n.id
WHERE n.exploration_id=$1 AND n.kind='intent' AND n.id=ANY($2::bigint[])
ORDER BY n.id`, s.expID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var route string
		if err := rows.Scan(&id, &route); err != nil {
			return nil, err
		}
		out[id] = route
	}
	return out, rows.Err()
}

// DerivedParentsForNodes reads the full parent identity of the displayed intent
// window in one query. It does not expand parent payloads or the whole graph.
func (s *ExplorationStore) DerivedParentsForNodes(ids []int64) (map[int64][]int64, error) {
	out := map[int64][]int64{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.db.Query(`SELECT dst_id, src_id FROM exploration_edges
WHERE exploration_id=$1 AND rel='derived_from' AND dst_id=ANY($2::bigint[])
ORDER BY dst_id, src_id`, s.expID, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var dst, src int64
		if err := rows.Scan(&dst, &src); err != nil {
			return nil, err
		}
		out[dst] = append(out[dst], src)
	}
	return out, rows.Err()
}
