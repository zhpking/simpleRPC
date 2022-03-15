package simpleRPC

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"simpleRPC/codec"
	"strings"
	"sync"
	"errors"
	"time"
)

type Call struct {
	Seq uint64
	ServiceMethod string // 远程调用path，格式 service.method
	Args interface{} // 远程调用的参数
	Reply interface{} // 远程调用完成后返回的数据
	Error error // 如果错误发生，返回错误类型
	Done chan *Call // 调用完成时注册一个通知事件
}

func (call *Call) done() {
	call.Done <- call
}

type Client struct {
	cc codec.Codec // 消息的编解码器，和服务端类似，用来序列化将要发送出去的请求，以及反序列化接收到的响应
	opt *Option
	sending sync.Mutex // 一个互斥锁，和服务端类似，为了保证请求的有序发送，即防止出现多个请求报文混淆
	header codec.Header // 每个请求的消息头，header 只有在请求发送时才需要，而请求发送是互斥的，因此每个客户端只需要一个，声明在 Client 结构体中可以复用
	mu sync.Mutex
	seq uint64 // 用于给发送的请求编号，每个请求拥有唯一编号
	pending map[uint64]*Call // 存储未处理完的请求，键是编号，值是 Call 实例
	closing bool // closing 和 shutdown 任意一个值置为 true，则表示 Client 处于不可用的状态，但有些许的差别，closing 是用户主动关闭的，即调用 Close 方法，而 shutdown 置为 true 一般是有错误发生
	shutdown bool
}

// 关闭连接
func (client *Client) Close() error {
	client.mu.Lock()
	defer client.mu.Unlock()

	if client.closing {
		return ErrShutdown
	}

	client.closing = true
	return client.cc.Close()
}

// 如果客户端在运行中的话就返回true
func (client *Client) IsAvailable() bool {
	client.mu.Lock()
	defer client.mu.Unlock()
	return !client.shutdown && !client.closing
}

var _ io.Closer = (*Client)(nil)

var ErrShutdown = errors.New("connection is shut down")

// 注册call元素
func (client *Client) registerCall(call *Call) (uint64, error) {
	client.mu.Lock()
	defer client.mu.Unlock()

	if client.closing || client.shutdown {
		return 0, ErrShutdown
	}
	call.Seq = client.seq
	client.pending[call.Seq] = call
	client.seq ++
	return call.Seq, nil
}

// 删除call元素
func (client *Client) removeCall(seq uint64) *Call {
	client.mu.Lock()
	defer client.mu.Unlock()
	call := client.pending[seq]
	delete(client.pending, seq)

	return call
}

// 服务端或客户端发生错误时调用，将 shutdown 设置为 true，且将错误信息通知所有 pending 状态的 call
func (client *Client) terminateCalls(err error) {
	client.sending.Lock()
	defer client.sending.Unlock()
	client.mu.Lock()
	defer client.mu.Unlock()
	client.shutdown = true
	for _, call := range client.pending {
		call.Error = err
		call.done()
	}
}

// 接受请求响应
func (client *Client) receive() {
	var err error
	for err == nil {
		var h codec.Header
		if err = client.cc.ReadHeader(&h); err != nil {
			break
		}
		call := client.removeCall(h.Seq)

		// switch 中的表达式是可选的，可以省略。
		// 如果省略表达式，则相当于 switch true，
		// 这种情况下会将每一个 case 的表达式的求值结果与 true 做比较，
		// 如果相等，则执行相应的代码。
		switch {
		case call == nil:
			// call不存在，可能是请求没有发送完整，或者因为其他原因被取消，但是服务端仍旧处理了
			err = client.cc.ReadBody(nil)
		case h.Error != "":
			// call 存在，但服务端处理出错，即 h.Error 不为空
			call.Error = fmt.Errorf(h.Error)
			err = client.cc.ReadBody(nil)
			call.done()
		default:
			err = client.cc.ReadBody(call.Reply)
			if err != nil {
				call.Error = errors.New("reading body " + err.Error())
			}
			call.done()
		}
	}

	// 如果错误发生，就挂起调用
	client.terminateCalls(err)
}

func NewClient(conn net.Conn, opt *Option) (*Client, error) {
	f := codec.NewCodecFuncMap[opt.CodecType]
	if f == nil {
		err := fmt.Errorf("invalid codec type %s", opt.CodecType)
		log.Println("rpc client: codec error:", err)
		return nil, err
	}

	// 发送options给服务端，约定好编码方式（交换协议）
	if err := json.NewEncoder(conn).Encode(opt); err != nil {
		log.Println("rpc client: options error:", err)
		_ = conn.Close()
		return nil, err
	}

	// 接受服务端交换完协议消息，接下来才进行信息的传递，不然有可能会发生粘包
	if err := json.NewDecoder(conn).Decode(opt); err != nil {
		log.Println("rpc client: options error:", err)
		_ = conn.Close()
		return nil, err
	}

	return newClientCodec(f(conn), opt), nil
}

func newClientCodec(cc codec.Codec, opt *Option) *Client {
	client := &Client{
		seq: 1,
		cc: cc,
		opt: opt,
		pending: make(map[uint64]*Call),
	}

	go client.receive()
	return client
}

func parseOptions(opts ...*Option) (*Option, error) {
	if len(opts) == 0 || opts[0] == nil {
		return DefaultOption, nil
	}

	if len(opts) != 1 {
		return nil, errors.New("number of options is more than 1")
	}

	opt := opts[0]
	opt.MagicNumber = DefaultOption.MagicNumber
	if opt.CodecType == "" {
		opt.CodecType = DefaultOption.CodecType
	}

	return opt, nil
}

// 客户端连接超时处理
type clientResult struct {
	client *Client
	err error
}

type newClientFunc func(conn net.Conn, opt *Option) (client *Client, err error)

func dialTimeout(f newClientFunc, network, address string, opts ...*Option) (client *Client, err error) {
	opt, err := parseOptions(opts...)
	if err != nil {
		return nil, err
	}

	conn, err := net.DialTimeout(network, address, opt.ConnectTimeout)
	if err != nil {
		// 建立连接超时
		return nil, err
	}

	defer func() {
		if err != nil {
			_ = conn.Close()
		}
	}()

	ch := make(chan clientResult)
	go func(){
		client, err := f(conn, opt)
		ch <- clientResult{client: client, err: err}
	}()

	if opt.ConnectTimeout == 0 {
		result := <-ch
		return result.client, result.err
	}

	select {
	case <- time.After(opt.ConnectTimeout):
		// 交换协议超时
		return nil, fmt.Errorf("rpc client: connect timeout: expect within %s ", opt.ConnectTimeout)
	case result := <-ch :
		return result.client, result.err
	}
}

func DialWithTimeout(network, address string, opts ...*Option) (client *Client, err error) {
	return dialTimeout(NewClient, network, address, opts...)
}

// 连接rpc服务端（没有超时处理）
func Dial(network, address string, opts ...*Option) (client *Client, err error) {
	opt, err := parseOptions(opts...)
	if err != nil {
		return nil, err
	}
	// 建立连接
	conn, err := net.Dial(network, address)
	if err != nil {
		return nil, err
	}

	defer func() {
		if client == nil {
			_ = conn.Close()
		}
	}()

	// 这时候返回的client，里面的对象cc其实就已经是gob.go 里面的NewGobCodec了
	return NewClient(conn, opt)
}

// 发送请求
func (client *Client) send(call *Call) {
	// 确保客户端能发送一个完整的请求
	client.sending.Lock()
	defer client.sending.Unlock()

	// 注册调用
	seq, err := client.registerCall(call)
	if err != nil {
		call.Error = err
		call.done()
		return
	}

	// 准备请求头
	client.header.ServiceMethod = call.ServiceMethod
	client.header.Seq = seq
	client.header.Error = ""

	// 编码和发送请求
	if err := client.cc.Write(&client.header, call.Args); err != nil {
		call := client.removeCall(seq)

		if call != nil {
			call.Error = err
			call.done()
		}
	}
}

// 异步调用
func (client *Client) Go(serviceMethod string, args, reply interface{}, done chan *Call) *Call {
	if done == nil {
		done = make(chan *Call, 10)
	} else if cap(done) == 0 {
		log.Panic("rpc client: done channel is unbuffered")
	}
	call := &Call{
		ServiceMethod: serviceMethod,
		Args: args,
		Reply: reply,
		Done: done,
	}
	client.send(call)
	return call
}

// 远程调用（没有超时机制）
func (client *Client) Call(serviceMethod string, args, reply interface{}) error {
	call := <-client.Go(serviceMethod, args, reply, make(chan *Call, 1)).Done
	return call.Error
}

// 远程调用（超时机制）
func (client *Client) CallWithTimeout(ctx context.Context, serviceMethod string, args, reply interface{}) error {
	// 用户可以使用 context.WithTimeout 创建具备超时检测能力的 context 对象来控制。
	/*
	例如：
	ctx, _ := context.WithTimeout(context.Background(), time.Second)
	var reply int
	err := client.Call(ctx, "Foo.Sum", &Args{1, 2}, &reply)
	*/

	call := client.Go(serviceMethod, args, reply, make(chan *Call, 1))
	select {
	case <-ctx.Done():
		client.removeCall(call.Seq)
		return errors.New("rpc client: call failed: " + ctx.Err().Error())
	case call := <-call.Done:
		return call.Error
	}
}




// http请求
func NewHTTPClient(conn net.Conn, opt *Option) (*Client, error) {
	_, _ = io.WriteString(conn, fmt.Sprintf("CONNECT %s HTTP/1.0\n\n", defaultRPCPath))

	// 在交换RPC协议之前,http请求一定要成功响应
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "CONNECT"})
	if err == nil && resp.Status == connected {
		return NewClient(conn, opt)
	}

	if err == nil {
		err = errors.New("unexpected HTTP response:" + resp.Status)
	}

	return nil, err
}

// DialHTTP 连接到指定网络地址的 HTTP RPC 服务器，侦听默认 HTTP RPC 路径
func DialHTTP(network, address string, opts ...*Option) (*Client, error) {
	return dialTimeout(NewHTTPClient, network, address, opts...)
}

func XDial(rpcAddr string, opts ...*Option) (*Client, error) {
	parts := strings.Split(rpcAddr, "@")
	if len(parts) != 2 {
		return nil, fmt.Errorf("rpc client err: wrong format '%s', expect protocol@addr", rpcAddr)
	}
	protocol, addr := parts[0], parts[1]
	switch protocol {
	case "http":
		return DialHTTP("tcp", addr, opts...)
	default:
		// return Dial(protocol, addr, opts...)
		return DialWithTimeout(protocol, addr, opts...)
	}
}