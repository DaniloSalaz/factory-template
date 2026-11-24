// Package postgres may leave exported identifiers undocumented: their contract is the port's.
package postgres

type Store struct{}

func (*Store) Get() {}

// A note on how this adapter does it need not start with the method's name.
func (*Store) Put() {}
