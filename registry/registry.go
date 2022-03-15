package registry

import (
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

type SimpleRegistry struct {
	timeout time.Duration
	mu sync.Mutex
	servers map[string]*ServerItem
}

type ServerItem struct {
	Addr string
	start time.Time
}

const (
	defaultPath = "/_simplerpc_/registry"
	defaultTimeout = time.Minute * 5
)

func New(timeout time.Duration) *SimpleRegistry {
	return &SimpleRegistry{
		servers:make(map[string]*ServerItem),
		timeout:timeout,
	}
}

var DefaultSimpleRegister = New(defaultTimeout)

// 添加服务
func (r *SimpleRegistry) putServer(addr string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.servers[addr]
	if s == nil {
		r.servers[addr] = &ServerItem{Addr: addr, start:time.Now()}
	} else {
		// 存在，则更新时间（每次心跳检测都会更新时间，防止过期）
		s.start = time.Now()
	}
}

// 返回可用服务列表
func (r *SimpleRegistry) aliveServers() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	var alive []string
	for addr, s := range r.servers {
		if r.timeout == 0 || s.start.Add(r.timeout).After(time.Now()) {
			alive = append(alive, addr)
		} else {
			delete(r.servers, addr)
		}
	}

	sort.Strings(alive)
	return alive
}

// 通过get方法 在header头返回所有的可用服务列表
// 通过post方法 在header头传递添加的服务地址
func (r *SimpleRegistry) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	switch req.Method {
	case "GET":
		fmt.Println(111111, r.aliveServers())
		w.Header().Set("X-Simplerpc-Servers", strings.Join(r.aliveServers(), ","))
	case "POST":
		addr := req.Header.Get("X-Simplerpc-Servers")
		fmt.Println(222222, addr)
		if addr == "" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		r.putServer(addr)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (r *SimpleRegistry) HandleHTTP(registryPath string) {
	http.Handle(registryPath, r)
	log.Println("rpc registry path:", registryPath)
}

func HandleHTTP() {
	DefaultSimpleRegister.HandleHTTP(defaultPath)
}

// 心跳接口
func Heartbeat(registry, addr string, duration time.Duration) {
	// 限制一下心跳发送时间，防止发送心跳检测的时候，服务早就过期了
	if duration == 0 || duration > defaultTimeout {
		duration = defaultTimeout - time.Duration(1) * time.Minute
	}
	var err error
	err = sendHeartbeat(registry, addr)
	go func() {
		t := time.NewTicker(duration)
		for err == nil {
			<- t.C
			err = sendHeartbeat(registry, addr)
		}
	}()
}

// 发送心跳检测，此步包含服务的注册
func sendHeartbeat(registry, addr string) error {
	log.Println(addr, "send heart beat to registry", registry)
	httpClient := &http.Client{}
	req, _ := http.NewRequest("POST", registry, nil)
	req.Header.Set("X-Simplerpc-Servers", addr)
	// 发送心跳检测，如果心跳检测失败，服务的start是不会更新的，5分钟之后就会失效
	if _, err := httpClient.Do(req); err != nil {
		log.Println("rpc server: heart beat err:", err)
		return err
	}

	return nil
}

