// Command api composes modules through their public API, and is the app hook's only
// importer.
package main

import (
	_ "chaskicode.dev/saas/app"
	_ "chaskicode.dev/saas/core/a"
	_ "chaskicode.dev/saas/core/b"
)

func main() {}
