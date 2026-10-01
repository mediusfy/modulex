package moda

// usersTableName is a bare table-name constant with no SQL keywords nearby
// — still a likely reference (e.g. used to build a query elsewhere) and
// must be flagged.
const usersTableName = "users" // want `violating database boundary`
