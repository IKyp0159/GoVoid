# GoVoid

Agente remoto para Windows con panel de control en Discord, y una herramienta de escritorio que genera una copia del agente por cliente a partir de tres datos: token de bot, id de servidor y webhook.

Go 1.22+. Sin CGO. Solo `discordgo` y `golang.org/x/sys`.

## Piezas

| Pieza | Qué es |
|---|---|
| `cmd/agent` | El agente. Se instala, conecta al gateway y atiende comandos desde el servidor. |
| `cmd/studio` | GoVoid Studio. GUI Win32 para generar el agente de un cliente. |
| `internal/kit` | Motor de compilación compartido por Studio y el builder de consola. |
| `cmd/builder` | Misma función que Studio, por terminal. |
| `cmd/winres` | Inyecta icono, manifiesto y versión en un exe ya compilado. |
| `cmd/panic` | Botón de pánico local (última salida si el bot deja de responder). |

## Compilar

```
build_studio.bat
```

Compila Studio, inyecta los recursos y deja una copia en `dist/`.

Para un agente con credenciales propias:

```
build_agent.bat
```

Lee `agent.local.env` (`GOVOID_TOKEN`, `GOVOID_GUILD`, `GOVOID_WEBHOOK`). Las credenciales nunca van al código.

## GoVoid Studio

Descarga el `.exe` desde [Releases](../../releases) y ejecútalo. No necesita instalador ni Go.

Tres campos: token del bot, id de la guild y URL del webhook. El identificador, el nombre del binario, la carpeta de salida, las tareas, el servicio y la RunKey se derivan del id de la guild.

`Probar conexión` valida los tres datos contra Discord antes de compilar. `Compilar agente` produce:

```
dist/<ID>/
  <id>.exe
  ENTREGA.txt
  SHA256SUMS.txt
  <nombre>-<id>-<fecha>.zip
```

El token se guarda cifrado con DPAPI en `%APPDATA%\GoVoidStudio\studio.json`.

## Comandos

El detalle de cada comando está en [docs/comandos.md](docs/comandos.md).

## Estructura

```
cmd/        binarios
internal/   bot, comandos, identidad, kit de compilación, win32
```

## Licencia

Uso privado. Ver `LICENSE`.
