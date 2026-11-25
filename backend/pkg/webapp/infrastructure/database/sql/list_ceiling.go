package sql

// listRunawayCeiling bounds every "return the whole collection" repository
// read. It is a RUNAWAY GUARD, not a product limit or pagination: the
// per-tenant caps (NODE_MAX_PER_ACCOUNT 500, SECRET_MAX_PER_ACCOUNT 5000,
// image counts in single digits) keep every legitimate collection far below
// it, so a query this ceiling actually binds on is reading a table something
// has gone wrong in — and before it existed, that read materialized the whole
// table into one response.
// The frontend pages these collections client-side, which is why the bound
// must sit well ABOVE any real collection rather than at a page size.
const listRunawayCeiling = 10000
