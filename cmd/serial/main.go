// Serial tool for communicating with Pico W over USB.
// Usage: go run ./cmd/serial [port] [command]
//
// If no command given, just reads output for 3 seconds.
// If command given, sends it and reads the response.
package main

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

func main() {
	port := "/dev/cu.usbmodem11301"
	if len(os.Args) > 1 {
		port = os.Args[1]
	}

	cmd := ""
	if len(os.Args) > 2 {
		cmd = strings.Join(os.Args[2:], " ")
	}

	f, err := os.OpenFile(port, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open %s: %v\n", port, err)
		os.Exit(1)
	}
	defer f.Close()

	// Configure raw serial 115200
	fd := f.Fd()
	var t syscall.Termios
	t.Cflag = syscall.CS8 | syscall.CREAD | syscall.CLOCAL | syscall.B115200
	t.Ispeed = syscall.B115200
	t.Ospeed = syscall.B115200
	t.Cc[syscall.VMIN] = 0
	t.Cc[syscall.VTIME] = 1
	// TIOCSETA on macOS
	const TIOCSETA = 0x80487414
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, TIOCSETA, uintptr(unsafe.Pointer(&t)))
	if errno != 0 {
		fmt.Fprintf(os.Stderr, "termios: %v\n", errno)
		os.Exit(1)
	}

	// Send command if given
	if cmd != "" {
		f.Write([]byte(cmd + "\r"))
	}

	// Read output
	buf := make([]byte, 4096)
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		f.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		n, _ := f.Read(buf)
		if n > 0 {
			os.Stdout.Write(buf[:n])
			deadline = time.Now().Add(10 * time.Second)
		}
	}
	fmt.Println()
}
