//go:build !linux && !darwin && !windows

package migrate

func runningProcesses() []string { return nil }
