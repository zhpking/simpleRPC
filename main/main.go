package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"simpleRPC"
	"simpleRPC/registry"
	"simpleRPC/xclient"
	"sync"
	"time"
)

type Foo int

type Args struct {
	Num1 int
	Num2 int
}

func (f Foo) Sum(args Args, reply *int) error {
	*reply = args.Num1 + args.Num2
	return nil
}

func (f Foo) Sleep(args Args, reply *int) error {
	// 测试一下连接超时
	time.Sleep(time.Second * time.Duration(args.Num1))
	*reply = args.Num1 + args.Num2
	return nil
}

// rpc demo
// 注册Foo到Server中，启动RPC服务
func startServer(addr chan string) {
	var foo Foo
	if err := simpleRPC.Register(&foo); err != nil {
		log.Fatal("network error:", err)
	}

	// 随机端口
	l, err := net.Listen("tcp", ":0")
	// l, err := net.Listen("tcp", ":36999")
	if err != nil {
		log.Fatal("network error:", err)
	}
	log.Println("start rpc server on", l.Addr())
	addr <- l.Addr().String()
	simpleRPC.Accept(l)
}

func rpcClientCall() {
	// 构造参数，发送RPC请求，并打印
	log.SetFlags(0)
	addr := make(chan string)
	go startServer(addr)
	client, _ := simpleRPC.Dial("tcp", <-addr)
	defer func() {
		_ = client.Close()
	}()

	// 解决粘包问题，这里sleep 1s的原因是要保证服务端能准确的读取Option，而不是Option|Header导致后续的RPC消息变成Body从而报错
	// 可以参考这个：https://github.com/geektutu/7days-golang/issues/26
	// time.Sleep(time.Second)

	var wg sync.WaitGroup
	for i := 0; i < 5; i ++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			args := &Args{Num1: i, Num2: i * i}
			var reply int
			if err := client.Call("Foo.Sum", args, &reply); err != nil {
				log.Fatal("call Foo.Sum error:", err)
			}
			log.Printf("%d + %d = %d", args.Num1, args.Num2, reply)
		}(i)
	}

	wg.Wait()
}

// 支持http协议demo
func startHTTPServer(addr chan string) {
	var foo Foo
	if err := simpleRPC.Register(&foo); err != nil {
		log.Fatal("network error:", err)
	}

	// 随机端口
	l, err := net.Listen("tcp", ":9999")
	// l, err := net.Listen("tcp", ":36999")
	if err != nil {
		log.Fatal("network error:", err)
	}
	simpleRPC.HandleHTTP()
	log.Println("start HTTP server on", l.Addr())
	addr <- l.Addr().String()
	_ = http.Serve(l, nil)
}

func httpClientCall(addr chan string) {
	// log.SetFlags(0)
	// addr := make(chan string)
	// go startHTTPServer(addr)
	client, _ := simpleRPC.DialHTTP("tcp", <-addr)
	// time.Sleep(time.Second)

	var wg sync.WaitGroup
	for i := 0; i < 5; i ++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			args := &Args{Num1: i, Num2: i * i}
			var reply int
			if err := client.Call("Foo.Sum", args, &reply); err != nil {
				log.Fatal("call Foo.Sum error:", err)
			}
			log.Printf("%d + %d = %d", args.Num1, args.Num2, reply)
		}(i)
	}

	wg.Wait()
}

// 负载均衡启动demo
func loadBalanceStartServer(addrCh chan string) {
	var foo Foo
	l, _ := net.Listen("tcp", ":0")
	server := simpleRPC.NewServer()
	_ = server.Register(&foo)
	addrCh <- l.Addr().String()
	server.Accept(l)
}

func foo(xc *xclient.XClient, ctx context.Context, typ, serviceMethod string, args *Args) {
	var reply int
	var err error
	switch typ {
	case "call":
		err = xc.Call(ctx, serviceMethod, args, &reply)
	case "broadcast":
		err = xc.Broadcast(ctx, serviceMethod, args, &reply)
	}

	if err != nil {
		log.Printf("%s %s error: %v", typ, serviceMethod, err)
	} else {
		log.Printf("%s %s success: %d + %d = %d", typ, serviceMethod, args.Num1, args.Num2, reply)
	}
}

func loadBalanceCall(addr1, addr2 string) {
	d := xclient.NewMultiServerDiscovery([]string{"tcp@" + addr1, "tcp@" + addr2})
	xc := xclient.NewXClient(d, xclient.RandomSelect, nil)
	defer func() {
		_ = xc.Close()
	}()

	// time.Sleep(time.Second)

	// 发送请求 && 接受响应
	var wg sync.WaitGroup
	for i := 0; i < 5; i ++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, _ := context.WithTimeout(context.Background(), time.Second*2)
			foo(xc, ctx, "call", "Foo.Sum", &Args{Num1: i, Num2: i * i})
		}(i)
	}

	wg.Wait()
}

func loadBalanceBroadcast(addr1, addr2 string) {
	d := xclient.NewMultiServerDiscovery([]string{"tcp@" + addr1, "tcp@" + addr2})
	xc := xclient.NewXClient(d, xclient.RandomSelect, nil)
	defer func() {
		_ = xc.Close()
	}()

	// time.Sleep(time.Second)

	var wg sync.WaitGroup
	for i := 0; i < 5; i ++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// 超时设置
			timeout := time.Second * 2
			ctx, _ := context.WithTimeout(context.Background(), timeout)

			// foo(xc, context.Background(), "broadcast", "Foo.Sum", &Args{Num1: i, Num2: i * i})
			foo(xc, ctx, "broadcast", "Foo.Sum", &Args{Num1: i, Num2: i * i})


			foo(xc, ctx, "broadcast", "Foo.Sleep", &Args{Num1: i, Num2: i * i})
		}(i)
	}

	wg.Wait()
}

// 注册中心demo
func startRegistry(wg *sync.WaitGroup) {
	l, _ := net.Listen("tcp", ":9999")
	registry.HandleHTTP()
	wg.Done()
	_ = http.Serve(l, nil)
}

func startRegistryCenterServer(registryAddr string, wg *sync.WaitGroup, port string) {
	var foo Foo
	// l, _ := net.Listen("tcp", ":0")
	l, _ := net.Listen("tcp", ":" + port)
	server := simpleRPC.NewServer()
	_ = server.Register(&foo)
	registry.Heartbeat(registryAddr, "tcp@" + l.Addr().String(), 0)
	wg.Done()
	server.Accept(l)
}

func startRegistryCenterCall(registry string) {
	d := xclient.NewSimpleRegistryDiscovery(registry, 0)
	xc := xclient.NewXClient(d, xclient.RandomSelect, nil)
	defer func() {
		_ = xc.Close()
	}()

	var wg sync.WaitGroup
	for i := 0; i < 5; i ++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			foo(xc, context.Background(), "call", "Foo.Sum", &Args{Num1: i, Num2: i * i})
		}(i)
	}

	wg.Wait()
}

func startRegistryCenterBroadcast(registry string) {
	d := xclient.NewSimpleRegistryDiscovery(registry, 0)
	xc := xclient.NewXClient(d, xclient.RandomSelect, nil)
	defer func() {
		_ = xc.Close()
	}()

	var wg sync.WaitGroup
	for i := 0; i < 5; i ++ {
		go func(i int) {
			defer wg.Done()
			foo(xc, context.Background(), "broadcast", "Foo.Sum", &Args{Num1: i, Num2: i * i})
			ctx, _ :=  context.WithTimeout(context.Background(), time.Second*2)
			foo(xc, ctx, "broadcast", "Foo.Sleep", &Args{Num1: i, Num2: i * i})
		}(i)
	}

	wg.Wait()
}


func main() {
	// rpc请求
	// rpcClientCall()


	// http请求
	// log.SetFlags(0)
	// ch := make(chan string)
	// go httpClientCall(ch)
	// startHTTPServer(ch)


	// 负载均衡
	/*
	log.SetFlags(0)
	ch1 := make(chan string)
	ch2 := make(chan string)

	// 启动两个服务
	go loadBalanceStartServer(ch1)
	go loadBalanceStartServer(ch2)

	addr1 := <- ch1
	addr2 := <- ch2

	// time.Sleep(time.Second * 2)
	loadBalanceCall(addr1, addr2)
	loadBalanceBroadcast(addr1, addr2)
	*/


	// 注册中心
	log.SetFlags(0)
	registryUrl := "http://localhost:9999/_simplerpc_/registry"
	// var wg sync.WaitGroup
	wg := sync.WaitGroup{}
	wg.Add(1)
	// 先启动注册中心
	go startRegistry(&wg)
	wg.Wait()
	time.Sleep(time.Second)

	wg.Add(2)
	// 启动两个服务
	go startRegistryCenterServer(registryUrl, &wg, "12333")
	go startRegistryCenterServer(registryUrl, &wg, "12334")
	wg.Wait()
	time.Sleep(time.Second)

	startRegistryCenterCall(registryUrl)
	startRegistryCenterBroadcast(registryUrl)
}
