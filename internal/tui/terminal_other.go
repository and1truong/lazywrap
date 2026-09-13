//go:build !linux && !darwin

package tui

import (
	"context"
	"errors"
)

func CheckTerminal() error {
	return errors.New("lazywrap tui is supported on Linux and macOS; use lazywrap for normal mode")
}
func terminalSize() (int, int)                                    { return 80, 24 }
func openTerminal(context.Context) (<-chan string, func(), error) { return nil, nil, CheckTerminal() }
