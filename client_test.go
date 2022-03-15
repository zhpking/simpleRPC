package simpleRPC

import (
	"context"
	"net"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestClient_dialTimeout(t *testing.T) {
	t.Parallel()
	l, _ := net.Listen("tcp", ":0")

	f := func(conn net.Conn, opt *Option) (client *Client, err error) {
		_ = conn.Close()
		time.Sleep(time.Second * 2)
		return nil, nil
	}

	t.Run("timeout", func(t *testing.T) {
		_, err := dialTimeout(f, "tcp", l.Addr().String(), &Option{ConnectTimeout:time.Second * 1})
		_assert(err != nil && strings.Contains(err.Error(), "connect timeout"), "expect a timeout error")

	})

	t.Run("0", func(t *testing.T) {
		_, err := dialTimeout(f, "tcp", l.Addr().String(), &Option{ConnectTimeout:0})
		_assert(err == nil, "0 means no limit")
	})
}

type Bar int
func (b Bar) Timeout(argv int, replay *int) error {
	time.Sleep(time.Second * 2)
	return nil
}

func startServer(addr chan string) {
	var b Bar
	_ = Register(&b)

	l, _ := net.Listen("tcp", ":0")
	addr <- l.Addr().String()
	Accept(l)
}

func TestClient_Call(t *testing.T) {
	t.Parallel()
	addrCh := make(chan string)
	go startServer(addrCh)
	addr := <-addrCh
	time.Sleep(time.Second)

	t.Run("client timeout", func(t *testing.T) {
		client, _ := DialWithTimeout("tcp", addr)
		ctx, _ := context.WithTimeout(context.Background(), time.Second)
		var reply int
		err := client.CallWithTimeout(ctx, "Bar.Timeout", 1, &reply)
		// fmt.Println(11111111, err)
		_assert(err != nil && strings.Contains(err.Error(), ctx.Err().Error()), "expect a timeout error")
	})

	t.Run("server handle timeout", func(t *testing.T) {
		client, _ := DialWithTimeout("tcp", addr, &Option{
			HandleTimeout: time.Second,
		})
		var reply int
		err := client.CallWithTimeout(context.Background(), "Bar.Timeout", 1, &reply)
		// fmt.Println(22222222, err)
		_assert(err != nil && strings.Contains(err.Error(), "handle timeout"), "expect a timeout error")
	})
}

func TestXDial(t *testing.T) {
	if runtime.GOOS == "linux" {
		ch := make(chan struct{})
		addr := "/tmp/simplerpc.sock"
		go func() {
			_ = os.Remove(addr)
			l, err := net.Listen("unix", addr)
			if err != nil {
				t.Fatal("failed to listen unix socket")
			}
			ch <- struct{}{}
			Accept(l)
		}()
		<-ch
		_, err := XDial("unix@" + addr)
		_assert(err == nil, "failed to connect unix socket")
	}

}
