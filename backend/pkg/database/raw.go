package database

// Raw exposes the underlying handle for World State scope/candidate SQL
// that is not yet in the generated Querier.
func (q *Queries) Raw() DBTX {
	return q.db
}
