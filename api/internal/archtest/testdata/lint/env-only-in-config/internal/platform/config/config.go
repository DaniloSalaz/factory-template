// Package config is the one place allowed to read the environment.
package config

import "os"

// AppEnv is allowed here.
func AppEnv() string { return os.Getenv("APP_ENV") }
