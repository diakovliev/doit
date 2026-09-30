//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package ui

import "os"

func terminalResizeNotifications() (<-chan os.Signal, func()) {
	return nil, func() {}
}
