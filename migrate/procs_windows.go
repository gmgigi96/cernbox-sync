package migrate

import (
	"os/exec"
	"strings"
	"syscall"
)

// runningProcesses lists the image names of the running processes (best
// effort).
func runningProcesses() []string {
	const createNoWindow = 0x08000000
	cmd := exec.Command("tasklist", "/FO", "CSV", "/NH")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var names []string
	for line := range strings.SplitSeq(string(out), "\n") {
		if name, _, _ := strings.Cut(strings.TrimSpace(line), ","); name != "" {
			names = append(names, strings.Trim(name, `"`))
		}
	}
	return names
}
