package migrate

import (
	"os/exec"
	"strings"
)

// runningProcesses lists the names of the running processes (best effort).
func runningProcesses() []string {
	out, err := exec.Command("ps", "-axco", "comm=").Output()
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(out)), "\n")
}
