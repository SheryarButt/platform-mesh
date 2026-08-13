/*
Copyright The Platform Mesh Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cli

import (
	"context"
	"net"
	"strings"
	"time"
)

// LoopbackSuffix is the reserved TLD of RFC 6761 §6.3.
const LoopbackSuffix = ".localhost"

// dialer resolves `.localhost` names to loopback, then dials normally.
//
// RFC 6761 reserves anything under `.localhost` for loopback, and resolvers
// SHOULD answer without consulting DNS. curl does; Go's resolver does not — it
// checks /etc/hosts, misses, asks DNS, and reports "no such host". The dev
// environment is named under `*.pm.localhost`, so without this every Go client
// fails where curl to the same URL works.
//
// Only the address is substituted; TLS still verifies against the original
// hostname, the same as `curl --resolve`.
func dialer(overrides map[string]string) func(context.Context, string, string) (net.Conn, error) {
	base := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}

	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return base.DialContext(ctx, network, addr)
		}

		if ip, ok := overrides[host]; ok {
			return base.DialContext(ctx, network, net.JoinHostPort(ip, port))
		}

		if isLoopbackName(host) {
			return base.DialContext(ctx, network, net.JoinHostPort("127.0.0.1", port))
		}

		return base.DialContext(ctx, network, addr)
	}
}

// isLoopbackName reports whether RFC 6761 reserves this name for loopback.
func isLoopbackName(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	return h == "localhost" || strings.HasSuffix(h, LoopbackSuffix)
}
