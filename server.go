package simpleRPC

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"io"
	"log"
	"net"
	"net/http"
	"reflect"
	"simpleRPC/codec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const MagicNumber = 0x3bef5c

type Option struct {
	MagicNumber int
	CodecType codec.Type

	ConnectTimeout time.Duration // 连接超时，0为不限
	HandleTimeout time.Duration // 处理请求超时，0为不限
}

var DefaultOption = &Option {
	MagicNumber: MagicNumber,
	CodecType: codec.GobType,

	ConnectTimeout: 10 * time.Second,
}

type Server struct {
	serviceMap sync.Map
}

func NewServer() *Server {
	return &Server{}
}

var DefaultServer = NewServer()

func (server *Server) Accept(lis net.Listener) {
	// for 循环等待 socket 连接建立
	for {
		// 等待客户端建立连接
		conn, err := lis.Accept()
		if err != nil {
			log.Println("rpc server: accept error:", err)
			return
		}

		// 开启子协程处理,处理过程交给了ServerConn方法
		go server.ServeConn(conn)
	}
}

func(server *Server) ServeConn(conn io.ReadWriteCloser) {
	defer func() {
		_ = conn.Close()
	}()

	var opt Option
	if err := json.NewDecoder(conn).Decode(&opt); err != nil {
		log.Println("rpc server: options error: ", err)
		return
	}

	if opt.MagicNumber != MagicNumber {
		log.Printf("rpc server: invalid magic number %x", opt.MagicNumber)
		return
	}

	// 根据CodeType得到对应的消息编解码器
	f := codec.NewCodecFuncMap[opt.CodecType]
	if f == nil {
		log.Printf("rpc server: invalid codec type %s", opt.CodecType)
		return
	}

	// server.serveCodec(f(conn), &opt)

	cc := f(conn)
	// 接受到option之后，立马返回通知客户端，告诉客户端服务端已经交换完协议了
	// 这一步也是为了防止粘包，如果直接调用server.serveCodec(f(conn), &opt)，会有Option|Header格式的报文回来
	if err := json.NewEncoder(conn).Encode(opt); err != nil {
		log.Println("rpc server: option error :", err)
		return
	}

	server.serveCodec(cc, &opt)
}

// struct{}表示struct类型，是一个无元素的结构体类型，通常在没有信息存储时使用。
// 优点是大小为0，不需要内存来存储struct {}类型的值
// 而struct{}{}表示struct类型的值，该值也是空
// struct{}{}是一个复合字面量，它构造了一个struct {}类型的值，该值也是空
// eg：
// type User struct {
// 	   Name string
// 	   Age  int
// }
// user := User{Name:"test", Age:1}
// 相当于
// user := struct {
//     Name string
//     Age int
// }{Name:"test", Age:1}
var invalidRequest = struct {}{}

func (server *Server) serveCodec(cc codec.Codec, opt *Option) {
	sending := new(sync.Mutex)
	wg := new(sync.WaitGroup)
	for {
		// 读取请求
		req, err := server.readRequest(cc)
		if err != nil {
			if req == nil {
				break;
			}
			req.h.Error = err.Error()
			// 出错了的话，回复请求
			server.sendResponse(cc, req.h, invalidRequest, sending)
			continue
		}
		wg.Add(1)
		// 处理请求
		// go server.handleRequest(cc, req, sending, wg)
		go server.handleRequestWithTimeout(cc, req, sending, wg, opt.HandleTimeout)
	}
	wg.Wait()
	_ = cc.Close()
}

// DefaultServer 是一个默认的 Server 实例，主要为了用户使用方便
func Accept(lis net.Listener) {
	DefaultServer.Accept(lis)
}




type request struct {
	h *codec.Header
	argv, replyv reflect.Value

	mtype *methodType
	scv *service
}

func (server *Server) readRequestHeader(cc codec.Codec) (*codec.Header, error) {
	var h codec.Header
	if err := cc.ReadHeader(&h); err != nil {
		if err != io.EOF && err != io.ErrUnexpectedEOF {
			log.Println("rpc server: read header error:", err)
		}
		return nil, err
	}
	return &h, nil
}

func (server *Server) readRequest(cc codec.Codec) (*request, error) {
	h, err := server.readRequestHeader(cc)
	if err != nil {
		return nil, err
	}

	req := &request{h: h}

	/*
	req.argv = reflect.New(reflect.TypeOf(""))
	if err = cc.ReadBody(req.argv.Interface()); err != nil {
		log.Println("rpc server: read argv err:", err)
	}
	*/

	// todo 处理客户端发送过来的数据
	req.scv, req.mtype, err = server.findService(h.ServiceMethod)
	if err != nil {
		return req, err
	}
	req.argv = req.mtype.newArgv()
	req.replyv = req.mtype.newReplyv()

	argvi := req.argv.Interface()
	if req.argv.Type().Kind() != reflect.Ptr {
		argvi = req.argv.Addr().Interface()
	}
	if err = cc.ReadBody(argvi); err != nil {
		log.Println("rpc server: read body err:", err)
		return req, err
	}



	return req, nil
}

func (server *Server) sendResponse(cc codec.Codec, h *codec.Header, body interface{}, sending *sync.Mutex) {
	sending.Lock()
	defer sending.Unlock()
	if err := cc.Write(h, body); err != nil {
		log.Println("rpc server: write response error:", err)
	}
}

func (server *Server) handleRequestWithTimeout(cc codec.Codec, req *request, sending *sync.Mutex, wg *sync.WaitGroup, timeout time.Duration) {
	defer wg.Done()
	called := make(chan struct{})
	sent := make(chan struct{})
	go func(){
		err := req.scv.call(req.mtype, req.argv, req.replyv)
		called <- struct{}{}
		if err != nil {
			req.h.Error = err.Error()
			server.sendResponse(cc, req.h, invalidRequest, sending)
			sent <- struct{}{}
			return
		}

		server.sendResponse(cc, req.h, req.replyv.Interface(), sending)
		sent <- struct{}{}
	}()

	if timeout == 0 {
		<-called
		<-sent
		return
	}

	select {
	case <-time.After(timeout):
		req.h.Error = fmt.Sprintf("rpc server: request handle timeout expect within %s", timeout)
		server.sendResponse(cc, req.h, invalidRequest, sending)
	case <-called:
		<-sent
	}
}

func (server *Server) handleRequest(cc codec.Codec, req *request, sending *sync.Mutex, wg *sync.WaitGroup) {
	defer wg.Done()
	// log.Println(req.h, req.argv.Elem())
	// req.replyv = reflect.ValueOf(fmt.Sprintf("simplerpc resp %d", req.h.Seq))


	// todo 远程调用
	err := req.scv.call(req.mtype, req.argv, req.replyv)
	if err != nil {
		req.h.Error = err.Error()
		server.sendResponse(cc, req.h, invalidRequest, sending)
		return
	}


	server.sendResponse(cc, req.h, req.replyv.Interface(), sending)
}

func (server *Server) Register(rcvr interface{}) error {
	s := newService(rcvr)
	if _, dup := server.serviceMap.LoadOrStore(s.name, s); dup {
		return errors.New("rpc: service already defined:" + s.name)
	}
	return nil
}

func (server *Server) findService(serviceMethod string) (svc *service, mtype *methodType, err error) {
	// 获取.的下标
	dot := strings.LastIndex(serviceMethod, ".")
	if dot < 0 {
		err = errors.New("rpc server: service/method request ill-formed: " + serviceMethod)
		return
	}
	serviceName, methodName := serviceMethod[:dot],serviceMethod[dot+1:]
	svci, ok := server.serviceMap.Load(serviceName)
	if !ok {
		err = errors.New("rpc server: can't find service " + serviceName)
		return
	}

	// 接口断言
	svc = svci.(*service)
	mtype = svc.method[methodName]
	if mtype == nil {
		err = errors.New("rpc server: can't find method " + methodName)
	}
	return
}

func Register(rcvr interface{}) error {
	return DefaultServer.Register(rcvr)
}





type methodType struct {
	method reflect.Method // 方法本身
	ArgType reflect.Type // 第一个参数的类型
	ReplyType reflect.Type // 第二个参数的类型
	numCalls uint64 // 后续统计方法调用次数时会用到
}

func (m *methodType) NumCalls() uint64 {
	return atomic.LoadUint64(&m.numCalls)
}

func (m *methodType) newArgv() reflect.Value {
	var argv reflect.Value
	// 获取参数类型，参数有可能是指针类型或者值类型
	// reflect.New(m.ArgType) 是创建m.ArgType的指针，如m.ArgType是int，则返回int的指针，即*int，这步操作等效于：new(int)
	// 参考：http://c.biancheng.net/view/117.html
	if m.ArgType.Kind() == reflect.Ptr {
		// 如果是指针类型，这个Elem是Type的Elem
		// 如果这个结构体是 type Test struct {Age int},对应反射实体是 t := &Test{10}
		// 那么argv返回的就是&{ 0}
		argv = reflect.New(m.ArgType.Elem())
	} else {
		// 非指针类型，根据反射类型对象创建类型实例，这个Elem是Value的Elem
		// 如果这个结构体是 type Test struct {Age int},对应反射实体是 t := Test{10}
		// 那么argv返回的就是{ 0}
		argv = reflect.New(m.ArgType).Elem()
	}

	return argv
}

func (m *methodType) newReplyv() reflect.Value {
	// replyv 必须是指针类型
	replyv := reflect.New(m.ReplyType.Elem())
	switch m.ReplyType.Elem().Kind() {
	case reflect.Map:
		replyv.Elem().Set(reflect.MakeMap(m.ReplyType.Elem()))
	case reflect.Slice:
		replyv.Elem().Set(reflect.MakeSlice(m.ReplyType.Elem(), 0, 0))
	}

	return replyv
}

type service struct {
	name string // 结构体名称（如WaitGroup）
	typ reflect.Type // 结构体的类型(指针的Value类型，因为nerService传的rcvr就是指针)
	rcvr reflect.Value // 结构体的实例本身(指针的Value类型，因为nerService传的rcvr就是指针)
	method map[string]*methodType // 存储映射的结构体的所有符合条件的方法
}

func newService(rcvr interface{}) *service {
	s := new(service)
	s.rcvr = reflect.ValueOf(rcvr)
	// rcvr可能是一个指针，通过Indirect可以返回它指向的对象的类型。不然的话，它的type就是reflect.Ptr
	// 现在要的是Value类型，而Indirect返回v持有的指针指向的值的Value。
	// 如果v持有nil指针，会返回Value零值；如果v不持有指针，会返回v
	// Type().Name():Value类型转Type类型，然后获取对象实例名（如对应结构体名字）
	s.name = reflect.Indirect(s.rcvr).Type().Name()
	s.typ = reflect.TypeOf(rcvr)
	if !ast.IsExported(s.name) {
		log.Fatalf("rpc server: %s is not a valid service name", s.name)
	}
	s.registerMethods()
	return s
}

func (s *service) registerMethods() {
	s.method = make(map[string]*methodType)
	for i := 0; i < s.typ.NumMethod(); i ++ {
		method := s.typ.Method(i)
		mType := method.Type
		// mType.NumIn() 方法的输入参数个数
		// mType.NumOut() 方法的返回值个数
		// 反射出来的对象参数，会比原来多一个对象自身参数，类似于python的self，java中的this
		if mType.NumIn() != 3 || mType.NumOut() != 1 {
			continue
		}
		// mType.Out(0):返回一个函数类型的第i个输出参数的类型
		// 这里判断的是返回值如果不是error类型的话就不符合条件
		if mType.Out(0) != reflect.TypeOf((*error)(nil)).Elem() {
			continue
		}

		// 校验第一个输入参数和第二个输入参数
		argType, replyType := mType.In(1), mType.In(2)
		if !isExportedOrBuiltinType(argType) || !isExportedOrBuiltinType(replyType) {
			continue
		}

		s.method[method.Name] = &methodType{
			method: method,
			ArgType: argType,
			ReplyType: replyType,
		}
		log.Printf("rpc server: register %s.%s\n", s.name, method.Name)
	}
}

func isExportedOrBuiltinType(t reflect.Type) bool {
	// 1. ast.IsExported：检测方法是否可导出（也就是是否为public类型）
	// 2. PkgPath返回类型的包路径，即明确指定包的import路径，如"encoding/base64"
	//    如果类型为内建类型(string, error)或未命名类型(*T, struct{}, []int)，会返回""
	//    如果对go的类型不了解，可以查看https://blog.csdn.net/wohu1104/article/details/106202792#:~:text=1.2%20%E6%9C%AA%E5%91%BD%E5%90%8D%E7%B1%BB%E5%9E%8B&text=Go%20%E8%AF%AD%E8%A8%80%E7%9A%84%E5%9F%BA%E6%9C%AC%E7%B1%BB%E5%9E%8B%E4%B8%AD%E7%9A%84%E5%A4%8D%E5%90%88%E7%B1%BB%E5%9E%8B%EF%BC%9A%E6%95%B0%E7%BB%84,%E9%83%BD%E6%98%AF%E6%9C%AA%E5%91%BD%E5%90%8D%E7%B1%BB%E5%9E%8B%E3%80%82

	// 如果是自定义结构体，t.Name()返回结构体名，ast.IsExported(t.Name())会返回true， 而t.PkgPath() 返回这个结构体的路径，如simpleRPC
	// 如果是*int，t.Name()返回""，ast.IsExported(t.Name())会返回false，而t.PkgPath() 返回""
	// 也可以参考下该链接： https://blog.csdn.net/weixin_38168590/article/details/102242540
	return ast.IsExported(t.Name()) || t.PkgPath() == ""
}

func (s *service) call(m *methodType, argv, replyv reflect.Value) error {
	atomic.AddUint64(&m.numCalls, 1)
	f := m.method.Func
	returnValues := f.Call([]reflect.Value{s.rcvr, argv, replyv})
	if errInter := returnValues[0].Interface(); errInter != nil {
		return errInter.(error)
	}
	return nil
}







// 处理http请求
const (
	connected = "200 Connected to Gee RPC"
	defaultRPCPath = "/_simplerpc_"
	defaultDebugPath = "/debug/simplerpc"
)

// ServeHTTP实现了httpHandler从而响应RPC请求
func (server *Server) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method != "CONNECT" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = io.WriteString(w, "405 must CONNECT\n")
		return
	}

	// 获取tcp套接字,http协议也是基于tcp协议的。
	// 注意下面的ServeConn 这个方法，他的参数是什么类型的,这就是他要从http链接中获取套接字的原因。
	// 参考：https://liqiang.io/post/hijack-in-go
	conn, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		log.Print("rpc hijacking", req.RemoteAddr, ": ", err.Error())
		return
	}

	_, _ = io.WriteString(conn, "HTTP/1.0 " + connected + "\n\n")
	server.ServeConn(conn)
}

func (server *Server) HandleHTTP() {
	http.Handle(defaultRPCPath, server)
	http.Handle(defaultDebugPath, debugHTTP{server})
	log.Println("rpc server debug path:", defaultDebugPath)
}

func HandleHTTP() {
	DefaultServer.HandleHTTP()
}

