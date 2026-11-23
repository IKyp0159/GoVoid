//go:build windows

package util

import "golang.org/x/sys/windows"

var (
	dnsapiDLL                 = windows.NewLazySystemDLL("dnsapi.dll")
	procDnsFlushResolverCache = dnsapiDLL.NewProc("DnsFlushResolverCache")
)

// flushResolverCache vacía la caché del cliente DNS de Windows.
//
// Es lo que separa "vuelve en segundos" de "vuelve en cinco minutos". El primer
// lookup después de un bugcheck falla porque la pila de red aún no está
// levantada; Windows cachea ese NXDOMAIN y lo sigue sirviendo hasta que expira
// el TTL del registro. DnsFlushResolverCache lo tira en el acto.
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
