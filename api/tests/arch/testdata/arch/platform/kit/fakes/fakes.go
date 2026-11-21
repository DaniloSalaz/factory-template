//go:build integration

// Package fakes is production code that imports a test helper. The build tag proves the
// rule also sees the files of the integration tests.
package fakes

import _ "chaskicode.dev/saas/tests/testkit"
