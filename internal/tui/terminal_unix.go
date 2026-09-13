//go:build linux || darwin

package tui

import (
	"context"
	"fmt"
	"golang.org/x/sys/unix"
	"os"
)

func CheckTerminal() error {
	if _, err := unix.IoctlGetTermios(int(os.Stdin.Fd()), getTermios); err != nil {
		return fmt.Errorf("lazywrap tui requires an interactive terminal (stdin): %w", err)
	}
	if _, err := unix.IoctlGetWinsize(int(os.Stdout.Fd()), unix.TIOCGWINSZ); err != nil {
		return fmt.Errorf("lazywrap tui requires an interactive terminal (stdout): %w", err)
	}
	return nil
}

func terminalSize() (int, int) {
	w, err := unix.IoctlGetWinsize(int(os.Stdout.Fd()), unix.TIOCGWINSZ)
	if err != nil || w.Col == 0 || w.Row == 0 {
		return 80, 24
	}
	return int(w.Col), int(w.Row)
}

func openTerminal(ctx context.Context) (<-chan string, func(), error) {
	fd := int(os.Stdin.Fd())
	old, err := unix.IoctlGetTermios(fd, getTermios)
	if err != nil {
		return nil, nil, err
	}
	raw := *old
	raw.Iflag &^= unix.BRKINT | unix.ICRNL | unix.INPCK | unix.ISTRIP | unix.IXON
	raw.Oflag &^= unix.OPOST
	raw.Cflag |= unix.CS8
	raw.Lflag &^= unix.ECHO | unix.ICANON | unix.IEXTEN | unix.ISIG
	raw.Cc[unix.VMIN] = 1
	raw.Cc[unix.VTIME] = 0
	if err := unix.IoctlSetTermios(fd, setTermios, &raw); err != nil {
		return nil, nil, err
	}
	readCtx, cancel := context.WithCancel(ctx)
	keys := make(chan string, 32)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(keys)
		buf := make([]byte, 256)
		for readCtx.Err() == nil {
			fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
			_, err := unix.Poll(fds, 100)
			if err == unix.EINTR {
				continue
			}
			if err != nil {
				return
			}
			if fds[0].Revents&(unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0 {
				return
			}
			if fds[0].Revents&unix.POLLIN == 0 {
				continue
			}
			n, err := unix.Read(fd, buf)
			if err != nil || n == 0 {
				return
			}
			select {
			case keys <- string(buf[:n]):
			case <-readCtx.Done():
				return
			}
		}
	}()
	fmt.Fprint(os.Stdout, "\x1b[?1049h\x1b[?25l")
	return keys, func() {
		cancel()
		<-done
		_ = unix.IoctlSetTermios(fd, setTermios, old)
		fmt.Fprint(os.Stdout, "\x1b[?25h\x1b[?1049l")
	}, nil
}
