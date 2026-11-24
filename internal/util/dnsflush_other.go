//go:build !windows

package util

func flushResolverCache() error { return nil }
