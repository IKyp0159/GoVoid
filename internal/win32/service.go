//go:build windows

package win32

import (
	"time"

	"golang.org/x/sys/windows/svc"

	"github.com/local/govoid/internal/config"
)

var (
	shieldServiceName = config.ServiceName
	shieldGuardTask   = config.ServiceTaskName
)

func ShieldServiceName() string { return shieldServiceName }

func ShieldGuardTaskName() string { return shieldGuardTask }

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

func shieldLoop() {
	for {
		if LockRequested() {
			_ = runHidden("schtasks.exe", "/run", "/tn", shieldGuardTask)
		}
		time.Sleep(2 * time.Second)
	}
}
