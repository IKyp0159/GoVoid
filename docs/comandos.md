# Comandos

Todos se ejecutan en el canal de sesión de la máquina y responden ahí mismo. El
panel publica además quince botones con las acciones más usadas, para no escribir
nada.

---

## `/shell <cmd>`

**Qué hace.** Lanza el comando en `cmd.exe`, captura stdout y stderr y devuelve
la salida troceada en mensajes si pasa del límite de Discord. Tiene tope de tiempo
por llamada y mata el árbol de procesos al expirar.

**Para qué.** Es la puerta general: consultar servicios, leer configuración, mover
archivos, lanzar cualquier cosa que no tenga comando propio. Con `/shell` se hace
casi todo lo demás a mano.

```
/shell whoami /priv
/shell ipconfig /all
/shell dir C:\Users
```

---

## `/screenshot`

**Qué hace.** Captura el escritorio del usuario activo y lo publica como PNG.

**Para qué.** Ver qué está pasando en la máquina sin interrumpir a nadie: si hay
una sesión abierta, si una ventana bloquea algo, si el equipo está en una pantalla
de bloqueo. Es la comprobación más rápida de que hay alguien delante.

---

## `/sysinfo`

**Qué hace.** Devuelve hostname, usuario, dominio, IP pública, versión y build de
Windows, arquitectura, uptime, privilegios actuales y si el proceso está elevado.

**Para qué.** Primer comando que se lanza en un equipo nuevo. Dice si hace falta
`/elevate` y con qué cuenta se está trabajando.

---

## `/ps list`

**Qué hace.** Lista procesos con PID, nombre y memoria, paginado.

**Para qué.** Encontrar qué está consumiendo la máquina o qué antivirus, EDR o
monitor tiene encima.

---

## `/ps kill <pid>`

**Qué hace.** Termina el proceso indicado y sus hijos.

**Para qué.** Quitar de en medio lo que estorba: un análisis en curso, un proceso
que bloquea un archivo, una ventana que no se puede cerrar desde `/shell`.

---

## `/keylog start` · `/keylog stop` · `/keylog dump`

**Qué hace.** Arranca un keylogger de bajo nivel con hook global de teclado, lo
detiene o publica lo capturado. Escribe a un archivo de la carpeta de datos y lo
trocea al publicarlo.

**Para qué.** Recuperar lo que se escribe fuera de los navegadores: paneles que no
guardan contraseña, clientes de escritorio, formularios internos. Lo que
`/passwords` no alcanza porque nunca pasó por un almacén del navegador.

---

## `/download <ruta>`

**Qué hace.** Sube un archivo al canal. Si la ruta es una carpeta, la comprime
primero. Trocea lo que pase del límite de Discord.

**Para qué.** Sacar evidencia: documentos, bases de datos, configuraciones,
capturas guardadas, el propio log del agente cuando algo falla.

---

## `/upload <url>`

**Qué hace.** Descarga una URL en la máquina, opcionalmente la ejecuta.

**Para qué.** Meter una herramienta que no está instalada, o actualizar el propio
agente sin volver a tocar la máquina.

---

## `/clip get` · `/clip set <texto>`

**Qué hace.** Lee o escribe el portapapeles de la sesión interactiva.

**Para qué.** `get` recupera lo último que se copió, que muchas veces es una
contraseña o un token. `set` deja un texto listo para pegar, útil cuando hay que
hacer algo delante de alguien sin teclear.

---

## `/msgbox <título> <texto> [icono]`

**Qué hace.** Abre un MessageBox en la sesión del usuario, con icono de info,
aviso o error.

**Para qué.** Hablarle a quien está delante de la pantalla sin que parezca que
viene de un programa raro. Un aviso de "sesión caducada, vuelve a entrar" delante
de un login falso mueve más que cualquier keylogger.

---

## `/webcam`

**Qué hace.** Captura un frame de la cámara por defecto y lo publica.

**Para qué.** Confirmar quién está delante del equipo, o simplemente si hay alguien.

---

## `/passwords`

**Qué hace.** Extrae cookies y credenciales guardadas de Chrome, Edge y Brave.
Descifra con DPAPI y AES-GCM según la versión del almacén, y devuelve cada entrada
con su URL de origen.

**Para qué.** Llevarse las sesiones ya abiertas sin volver a pasar por un login.
Con las cookies se entra a los paneles sin contraseña ni 2FA, porque la sesión ya
está validada.

---

## `/lockscreen on [minutos]` · `/lockscreen off` · `/lockscreen status`

**Qué hace.** Pinta una cortina negra a pantalla completa, se queda por encima de
todo y bloquea teclado y ratón. Con minutos se libera sola. `off` la levanta,
`status` dice si está activa.

**Para qué.** Congelar la máquina mientras se trabaja en ella. La persona ve una
pantalla negra y no puede hacer nada; con `/screenshot` y `/shell` se sigue
operando por detrás.

---

## `/elevate`

**Qué hace.** Pide UAC y relanza el agente como administrador. El padre espera el
aviso del hijo antes de retirarse, para que no queden dos copias peleando.

**Para qué.** Todo lo que necesita administrador: servicio, tareas como SYSTEM,
volcado de SAM, control total. Sin elevar, el agente vive en la sesión del usuario.

---

## `/bsod [diagnostico]`

**Qué hace.** Fuerza un bugcheck matando un proceso crítico. Con `diagnostico`
solo comprueba que el mecanismo funciona, sin provocarlo.

**Para qué.** Apagar el equipo de golpe sin apagarlo: un bugcheck no da cierre
limpio, así que no hay volcado ordenado ni aviso a la persona. El agente vuelve
solo con la tarea de arranque.

---

## `/kill`

**Qué hace.** Apaga el agente hasta el próximo arranque de Windows. La persistencia
sigue puesta.

**Para qué.** Silenciarlo un rato sin desarmarlo. Vuelve solo en el siguiente logon.

---

## `/shutdown`

**Qué hace.** Apaga el agente y borra toda la persistencia: tareas, RunKey y
servicio.

**Para qué.** Retirada limpia. Después de esto la máquina queda como estaba, salvo
los archivos que se hayan llevado.

---

# Cómo funciona el agente

**Arranque.** Una instancia por equipo, garantizada con un mutex con nombre
derivado del identificador del cliente. Si el mutex ya existe, la copia sale sin
tocar nada.

**Sesión.** Al conectar crea o reutiliza un canal `sesion-N` en la guild, publica
un embed de estado y el panel de botones. Ese canal es el contrato: cada equipo
tiene el suyo y los comandos se atienden ahí.

**Persistencia.** Cuatro capas independientes:

- RunKey en `HKCU` para el logon.
- Tarea programada de logon.
- Tarea programada de arranque, como SYSTEM, que localiza la sesión de consola y
  lanza el agente dentro de ella. Es la que lo devuelve después de un bugcheck sin
  depender de que alguien inicie sesión.
- Servicio de Windows como respaldo.

Un watchdog revisa cada minuto que el proceso siga vivo y lo relanza si no. Las
copias sobrantes mueren en el mutex.

**Reconexión.** Si el gateway cae, se reintenta con espera creciente. Un watchdog
de red fuerza la reconexión cuando el socket queda abierto pero mudo, que es el
fallo silencioso típico de una sesión de Discord zombi.

**Identidad.** Todo lo que distingue una copia de otra —nombre del ejecutable,
servicio, tareas, clave de registro, carpeta de datos, mutex y clase de ventana—
sale de `internal/config` y se fija al compilar con `-ldflags -X`. Dos clientes en
el mismo equipo no se pisan.

**Credenciales.** El token va dentro del binario, nunca se lee de disco. Cada
cliente necesita su propia aplicación de Discord: el gateway admite una sola
sesión por token y dos agentes con el mismo se expulsan en bucle.

**Superficie.** No abre puertos ni escucha conexiones. Todo el tráfico sale hacia
el gateway y hacia el webhook de avisos; las respuestas viajan como mensajes del
canal. Sin tráfico entrante no hay puerto que escanear.
