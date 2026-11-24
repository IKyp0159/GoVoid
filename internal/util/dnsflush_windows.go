//go:build windows

package util

import "golang.org/x/sys/windows"

var (
	dnsapiDLL                 = windows.NewLazySystemDLL("dnsapi.dll")
	procDnsFlushResolverCache = dnsapiDLL.NewProc("DnsFlushResolverCache")
)

func flushResolverCache() error {
	if err := dnsapiDLL.Load(); err != nil {
		return err
	}
	if err := procDnsFlushResolverCache.Find(); err != nil {
		return err
	}
	r1, _, callErr := procDnsFlushResolverCache.Call()
	if r1 == 0 {
		return callErr
	}
	return nil
}
