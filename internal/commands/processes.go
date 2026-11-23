//go:build windows

package commands

import (
	"fmt"
	"strconv"
	"strings"
	"unsafe"

	"github.com/bwmarrin/discordgo"
	"github.com/local/govoid/internal/ui"
	"github.com/local/govoid/internal/win32"
	"golang.org/x/sys/windows"
)

const processPageSize = 24

type procInfo struct {
	PID  uint32
	Name string
}

// Processes atiende /ps list y /ps kill.
func Processes(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if err := Defer(s, i); err != nil {
		return
	}

	switch StringOption(i, "action") {
	case "kill":
		pid := IntOption(i, "pid")
		if pid <= 0 {
			Finish(s, i, "❌ Indica un PID válido.")
			return
		}
		killAndReport(s, i, uint32(pid))
	default:
		showProcessList(s, i)
	}
}

// ListProcesses atiende el botón del panel: muestra la lista de procesos.
func ListProcesses(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if err := Defer(s, i); err != nil {
		return
	}
	showProcessList(s, i)
}

// ProcessPage muestra la página pedida desde los botones de navegación.
func ProcessPage(s *discordgo.Session, i *discordgo.InteractionCreate, page int) {
	if err := Defer(s, i); err != nil {
		return
	}
	showProcessPage(s, i, page)
}

// ProcessSelected muestra el detalle del proceso elegido en el select menu.
func ProcessSelected(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if err := Defer(s, i); err != nil {
		return
	}

	values := i.MessageComponentData().Values
	if len(values) == 0 {
		Finish(s, i, "❌ No se seleccionó ningún proceso.")
		return
	}
	pid, err := strconv.ParseUint(values[0], 10, 32)
	if err != nil {
		Finish(s, i, "❌ PID inválido.")
		return
	}

	content := fmt.Sprintf("📂 Proceso seleccionado: PID **%d**", pid)
	components := []discordgo.MessageComponent{
		discordgo.ActionsRow{Components: []discordgo.MessageComponent{
			ui.Button("⏻ Matar proceso", discordgo.DangerButton, fmt.Sprintf("btn_pskill_%d", pid)),
		}},
	}
	if _, err := s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content:    &content,
		Components: &components,
	}); err != nil {
		return
	}
}

// KillProcessFromButton mata el proceso indicado en el CustomID del botón.
func KillProcessFromButton(s *discordgo.Session, i *discordgo.InteractionCreate, pid uint32) {
	if err := Defer(s, i); err != nil {
		return
	}
	killAndReport(s, i, pid)
}

func showProcessList(s *discordgo.Session, i *discordgo.InteractionCreate) {
	procs, err := snapshotProcesses()
	if err != nil {
		Finish(s, i, "❌ No se pudo listar procesos: "+err.Error())
		return
	}
	if len(procs) == 0 {
		Finish(s, i, "ℹ️ No se encontraron procesos.")
		return
	}

	if len(procs) <= 25 {
		var builder strings.Builder
		for _, proc := range procs {
			fmt.Fprintf(&builder, "%-42s %6d\n", truncate(proc.Name, 40), proc.PID)
		}
		Finish(s, i, fmt.Sprintf("📂 Procesos (%d):\n```\n%s```", len(procs), builder.String()))
		return
	}

	showProcessPage(s, i, 0)
}

func showProcessPage(s *discordgo.Session, i *discordgo.InteractionCreate, page int) {
	procs, err := snapshotProcesses()
	if err != nil {
		Finish(s, i, "❌ No se pudo listar procesos: "+err.Error())
		return
	}
	if len(procs) == 0 {
		Finish(s, i, "ℹ️ No se encontraron procesos.")
		return
	}

	totalPages := (len(procs) + processPageSize - 1) / processPageSize
	if page < 0 {
		page = 0
	}
	if page >= totalPages {
		page = totalPages - 1
	}

	start := page * processPageSize
	end := start + processPageSize
	if end > len(procs) {
		end = len(procs)
	}

	options := make([]discordgo.SelectMenuOption, 0, end-start)
	for _, proc := range procs[start:end] {
		options = append(options, discordgo.SelectMenuOption{
			Label:       truncate(proc.Name, 90),
			Value:       strconv.FormatUint(uint64(proc.PID), 10),
			Description: fmt.Sprintf("PID %d", proc.PID),
		})
	}

	components := ui.ProcessSelect(page, options)
	if nav := ui.ProcessNav(page, totalPages); nav != nil {
		components = append(components, nav...)
	}

	content := fmt.Sprintf("📂 Procesos — %d en total, página %d/%d", len(procs), page+1, totalPages)
	if _, err := s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content:    &content,
		Components: &components,
	}); err != nil {
		return
	}
}

func killAndReport(s *discordgo.Session, i *discordgo.InteractionCreate, pid uint32) {
	// TerminatePID activa SeDebugPrivilege: sin él, matar un proceso de SYSTEM
	// devuelve acceso denegado aunque la sesión sea de administrador.
	if err := win32.TerminatePID(pid); err != nil {
		Finish(s, i, fmt.Sprintf("❌ No se pudo terminar PID %d: %v", pid, err))
		return
	}
	Finish(s, i, fmt.Sprintf("✅ Proceso %d terminado.", pid))
}

func snapshotProcesses() ([]procInfo, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := windows.CloseHandle(snapshot); cerr != nil {
			_ = cerr
		}
	}()

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	if err := windows.Process32First(snapshot, &entry); err != nil {
		return nil, err
	}

	procs := make([]procInfo, 0, 256)
	for {
		procs = append(procs, procInfo{
			PID:  entry.ProcessID,
			Name: windows.UTF16ToString(entry.ExeFile[:]),
		})
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			break
		}
	}
	return procs, nil
}
