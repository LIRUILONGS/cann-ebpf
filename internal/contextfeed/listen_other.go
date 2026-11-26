//go:build !linux

package contextfeed

import (
	"fmt"
	"net"
)

func listenUnix(socketPath string, _ StartOptions) (net.Listener, []string, error) {
	return nil, nil, fmt.Errorf("Unix context socket is not supported on this platform")
}

func authorizePeer(conn net.Conn, _ map[uint32]struct{}) error {
	return nil
}
