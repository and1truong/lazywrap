//go:build darwin

package tui

import "golang.org/x/sys/unix"

const getTermios = unix.TIOCGETA
const setTermios = unix.TIOCSETA
