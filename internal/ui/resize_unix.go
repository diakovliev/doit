//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package ui

import (
	"os"
	"os/signal"
	"syscall"
)

func terminalResizeNotifications() (<-chan os.Signal, func()) {
	notifications := make(chan os.Signal, 1)
	signal.Notify(notifications, syscall.SIGWINCH)
	return notifications, func() { signal.Stop(notifications) }
}
