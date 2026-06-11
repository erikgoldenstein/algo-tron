package main

import (
	"bufio"
	"strings"
	"testing"
)

func TestReadProxyProtocolIP(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantIP  string
		wantErr bool
	}{
		{"TCP4", "PROXY TCP4 1.2.3.4 5.6.7.8 100 200\r\n", "1.2.3.4", false},
		{"TCP6", "PROXY TCP6 ::1 ::2 100 200\r\n", "::1", false},
		{"UNKNOWN", "PROXY UNKNOWN\r\n", "", false},
		{"invalid source IP", "PROXY TCP4 notanip 5.6.7.8 100 200\r\n", "", true},
		{"too few fields", "PROXY TCP4 1.2.3.4\r\n", "", true},
		{"not a PROXY header", "GET / HTTP/1.1\r\n", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := bufio.NewReader(strings.NewReader(c.input))
			ip, err := readProxyProtocolIP(r)
			if (err != nil) != c.wantErr {
				t.Errorf("err = %v, wantErr = %v", err, c.wantErr)
			}
			if ip != c.wantIP {
				t.Errorf("ip = %q, want %q", ip, c.wantIP)
			}
		})
	}
}
