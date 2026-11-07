//go:build linux || darwin

package storage

import (
	"context"
	"net"
	"syscall"
	"testing"
	"time"

	"github.com/steviebps/realm/pkg/storage/pgtest"
)

// TestPostgresKeepAlive checks that connections to the database probe an
// idle peer at the interval and count in keepAlive, rather than the operating
// system's defaults, which take minutes to notice a host that went away.
func TestPostgresKeepAlive(t *testing.T) {
	ctx := context.Background()
	s := newPostgres(t, pgtest.URL(t))
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	tcp, ok := conn.Conn().PgConn().Conn().(*net.TCPConn)
	if !ok {
		t.Skipf("the connection is a %T, not TCP", conn.Conn().PgConn().Conn())
	}
	raw, err := tcp.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var on, interval, count int
	var serr error
	if err := raw.Control(func(fd uintptr) {
		if on, serr = syscall.GetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_KEEPALIVE); serr != nil {
			return
		}
		if interval, serr = syscall.GetsockoptInt(int(fd), syscall.IPPROTO_TCP, syscall.TCP_KEEPINTVL); serr != nil {
			return
		}
		count, serr = syscall.GetsockoptInt(int(fd), syscall.IPPROTO_TCP, syscall.TCP_KEEPCNT)
	}); err != nil || serr != nil {
		t.Fatal(err, serr)
	}
	if on == 0 || interval != int(keepAlive.Interval/time.Second) || count != keepAlive.Count {
		t.Errorf("keepalive on=%d interval=%ds count=%d; want on, %v, %d", on, interval, count, keepAlive.Interval, keepAlive.Count)
	}
}
