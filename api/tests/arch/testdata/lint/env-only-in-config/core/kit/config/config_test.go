package config

import (
	"os"
	"testing"
)

func TestAllowedInConfigTests(t *testing.T) {
	if _, ok := os.LookupEnv("APP_ENV"); ok {
		t.Log("allowed in config tests")
	}
}
