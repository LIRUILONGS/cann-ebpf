//go:build linux

package contextfeed

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func listenUnix(socketPath string, options StartOptions) (net.Listener, []string, error) {
	if socketPath == "" {
		return nil, nil, fmt.Errorf("empty context socket path")
	}
	dir := filepath.Dir(socketPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, nil, fmt.Errorf("create context socket directory: %w", err)
	}
	var warnings []string
	if err := os.Chmod(dir, 0o755); err != nil {
		warnings = append(warnings, "context socket directory chmod 0755: "+err.Error())
	}
	if info, err := os.Lstat(socketPath); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, nil, fmt.Errorf("context socket path exists and is not a socket: %s", socketPath)
		}
		if conn, dialErr := net.DialTimeout("unix", socketPath, 200*time.Millisecond); dialErr == nil {
			_ = conn.Close()
			return nil, nil, fmt.Errorf("context socket already has an active listener: %s", socketPath)
		}
		if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Uid != uint32(os.Geteuid()) {
			allowed := false
			for _, uid := range options.AllowedUIDs {
				if stat.Uid == uid {
					allowed = true
					break
				}
			}
			if !allowed {
				return nil, nil, fmt.Errorf("stale context socket is owned by uid %d, not an allowed uid", stat.Uid)
			}
		}
		if err := os.Remove(socketPath); err != nil {
			return nil, nil, fmt.Errorf("remove stale context socket: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return nil, nil, fmt.Errorf("inspect context socket: %w", err)
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, nil, fmt.Errorf("listen context socket: %w", err)
	}
	mode := os.FileMode(0o600)
	if options.Chown {
		if err := os.Chown(socketPath, int(options.OwnerUID), int(options.OwnerGID)); err != nil {
			_ = listener.Close()
			_ = os.Remove(socketPath)
			return nil, warnings, fmt.Errorf("chown context socket to %d:%d: %w", options.OwnerUID, options.OwnerGID, err)
		}
		mode = 0o660
	}
	if err := os.Chmod(socketPath, mode); err != nil {
		_ = listener.Close()
		_ = os.Remove(socketPath)
		return nil, warnings, fmt.Errorf("chmod context socket %04o: %w", mode, err)
	}
	return listener, warnings, nil
}

func authorizePeer(conn net.Conn, allowed map[uint32]struct{}) error {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return fmt.Errorf("context connection is not Unix")
	}
	raw, err := unixConn.SyscallConn()
	if err != nil {
		return fmt.Errorf("context peer syscall conn: %w", err)
	}
	var peer *unix.Ucred
	var socketErr error
	if err := raw.Control(func(fd uintptr) {
		peer, socketErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return fmt.Errorf("context peer control: %w", err)
	}
	if socketErr != nil {
		return fmt.Errorf("context peer credentials: %w", socketErr)
	}
	if peer == nil {
		return fmt.Errorf("context peer credentials unavailable")
	}
	if _, ok := allowed[peer.Uid]; !ok {
		return fmt.Errorf("context peer uid %d is not allowed", peer.Uid)
	}
	return nil
}
