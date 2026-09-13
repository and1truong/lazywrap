//go:build linux

package tui

import "golang.org/x/sys/unix"

const getTermios = unix.TCGETS
const setTermios = unix.TCSETS
