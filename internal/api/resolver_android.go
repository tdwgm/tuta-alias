//go:build android

package api

import (
	"context"
	"net"
	"os"
)

// Android has no /etc/resolv.conf, so Go's own resolver (this binary is built
// with CGO_ENABLED=0) falls back to [::1]:53 and every lookup fails with
// "read udp [::1]:…->[::1]:53: connection refused". Termux keeps its resolver
// config in $PREFIX/etc/resolv.conf and patches its own Go toolchain to read
// it (termux-packages packages/golang/patch-script/fix-hardcoded-etc-resolv-conf.diff);
// a binary cross-compiled with stock Go needs the same redirect at runtime.
// Go still parses its defaults; only the server it dials is replaced.
func init() {
	if _, err := os.Stat("/etc/resolv.conf"); err == nil {
		return // a system resolv.conf exists: leave Go's resolver alone
	}
	servers := termuxNameservers()
	if len(servers) == 0 {
		return
	}
	var d net.Dialer
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var err error
			for _, s := range servers {
				var c net.Conn
				if c, err = d.DialContext(ctx, network, net.JoinHostPort(s, "53")); err == nil {
					return c, nil
				}
			}
			return nil, err
		},
	}
}
