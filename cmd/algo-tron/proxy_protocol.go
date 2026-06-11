package main

import (
	"bufio"
	"fmt"
	"net"
	"strings"
)

func readProxyProtocolIP(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	fields := strings.Fields(strings.TrimRight(line, "\r\n"))
	if len(fields) < 2 || fields[0] != "PROXY" {
		return "", fmt.Errorf("expected PROXY protocol header")
	}
	if fields[1] == "UNKNOWN" {
		return "", nil
	}
	if len(fields) < 6 {
		return "", fmt.Errorf("invalid PROXY protocol header")
	}
	ip := fields[2]
	if net.ParseIP(ip) == nil {
		return "", fmt.Errorf("invalid source IP in PROXY header: %q", ip)
	}
	return ip, nil
}
