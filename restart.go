package app

import "syscall"

func Restart() error {
	return syscall.Kill(syscall.Getpid(), syscall.SIGUSR1)
}
