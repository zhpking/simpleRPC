package xclient

import (
	"errors"
	"math"
	"math/rand"
	"sync"
	"time"
)

type SelectMode int

// 定义负载均衡策略
const (
	RandomSelect SelectMode = iota // 随机
	RoundRobinSelect // 轮询
)

type Discovery interface {
	Refresh() error // 从注册中心更新服务列表
	Update(servers []string) error // 手动更新服务列表
	Get (mode SelectMode) (string, error) // 根据负载均衡策略，选择一个服务实例
	GetAll() ([]string, error) // 返回所有的服务实例
}

// 一个简单的注册中心（手动维护一个服务地址来代替注册中心）
type MultiServersDiscovery struct {
	r *rand.Rand // 生成随机数
	mu sync.RWMutex
	servers []string // 服务地址
	index int // 记录算法轮询到的位置
}

// 因为是需要手动配置的，所以刷新功能暂时不需要
func (d *MultiServersDiscovery) Refresh() error {
	return nil
}

// 动态更新服务发现
func (d *MultiServersDiscovery) Update(servers []string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.servers = servers
	return nil
}

// 通过负载均衡策略获取服务地址
func (d *MultiServersDiscovery) Get(mode SelectMode) (string, error){
	d.mu.Lock()
	defer d.mu.Unlock()
	n := len(d.servers)
	if n == 0 {
		return "", errors.New("rpc discovery: no available servers")
	}

	switch mode {
	case RandomSelect:
		return d.servers[d.r.Intn(n)], nil
	case RoundRobinSelect:
		s := d.servers[d.index%n]
		d.index = (d.index + 1) % n
		return s, nil
	default:
		return "", errors.New("rpc discovery: not supported select mode")
	}
}

// 返回所有的服务地址
func (d *MultiServersDiscovery) GetAll() ([]string, error) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	servers := make([]string, len(d.servers), len(d.servers))
	copy(servers, d.servers)
	return servers, nil
}

// 创建MultiServersDiscovery实例
func NewMultiServerDiscovery(servers []string) *MultiServersDiscovery {
	d := &MultiServersDiscovery{
		servers: servers,
		r: rand.New(rand.NewSource(time.Now().UnixNano())),
	}
	// 初始化的时候使用随机数，这样就不会每次都从0开始轮询
	d.index = d.r.Intn(math.MaxInt32 - 1)
	return d
}

// 定义 MultiServersDiscovery 必须要实现 Discovery 接口
var _ Discovery = (*MultiServersDiscovery)(nil)
