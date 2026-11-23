//go:build windows

package win32

import (
	"time"

	"golang.org/x/sys/windows/svc"

	"github.com/local/govoid/internal/config"
)

// shieldServiceName es el nombre que ve el SCM. Con failure actions a 0 ms, el
// servicio vuelve a arrancar antes de que el operador note el hueco.
//
// shieldGuardTask relanza el agente cada minuto desde el Programador de tareas,
// fuera del alcance del proceso del agente. Los dos nombres llevan el ProductID
// del cliente para que dos instalaciones distintas en la misma máquina no
// compartan servicio ni tarea.
var (
	shieldServiceName = config.ServiceName
	shieldGuardTask   = config.ServiceTaskName
)

// ShieldServiceName es el nombre del servicio en el SCM.
func ShieldServiceName() string { return shieldServiceName }

// ShieldGuardTaskName es el nombre de la tarea que relanza el agente.
func ShieldGuardTaskName() string { return shieldGuardTask }

// RunService entra en el despachador de servicios. Se llama con --service y
// no vuelve hasta que el SCM manda parar.
//
// El servicio corre en la sesión 0, así que no puede dibujar la cortina. Su
// trabajo es distinto: mantener vivo al agente que sí puede, disparando la
// tarea que corre en la sesión interactiva.
func RunService() error {
	return svc.Run(shieldServiceName, &shieldHandler{})
}

type shieldHandler struct{}

func (h *shieldHandler) Execute(_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}

	go shieldLoop()

	status <- svc.Status{
		State:   svc.Running,
		Accepts: svc.AcceptStop | svc.AcceptShutdown,
	}

	for request := range requests {
		switch request.Cmd {
		case svc.Interrogate:
			status <- request.CurrentStatus
		case svc.Stop, svc.Shutdown:
			status <- svc.Status{State: svc.StopPending}
			return false, 0
		}
	}

	return false, 0
}

// shieldLoop dispara la tarea del centinela cada dos segundos mientras la
// máquina tenga que estar bloqueada. Si el candado está levantado, duerme.
func shieldLoop() {
	for {
		if LockRequested() {
			_ = runHidden("schtasks.exe", "/run", "/tn", shieldGuardTask)
		}
		time.Sleep(2 * time.Second)
	}
}
