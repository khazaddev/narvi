// Package shadowledger records what platform shadow mode did not do.
//
// Shadow mode's product is the record (§30.6): the platform runs against
// real customer repositories, every customer-visible write is suppressed,
// and what an operator evaluates afterwards is the ledger of effects that
// would have happened. This package owns that write path -- the token-free
// record types that bound what can reach the table, and the record-or-fail
// insert that refuses to let a suppression go unevidenced.
package shadowledger
