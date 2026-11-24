// Package foo breaks the rule: outside adapters/, every exported identifier needs a doc comment.
package foo

func Undocumented() {}

// Misnamed: outside adapters/, a doc comment starts with the identifier's name.
func Misnamed() {}
