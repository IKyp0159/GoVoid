//go:build !windows

package util

// flushResolverCache no existe fuera de Windows; el módulo es Windows-only,
// pero el archivo mantiene el paquete compilable en cualquier GOOS.
func flushResolverCache() error { return nil }
