package xclient

import (
	"log"
	"net/http"
	"strings"
	"time"
)

type SimpleRegistryDiscovery struct {
	*MultiServersDiscovery
	registry string // 注册中心url
	timeout time.Duration // 服务列表过期时间
	lastUpdate time.Time // 最后从注册中心拉取服务配置时间，超过了该时间，需要去注册中心从新拉取服务配置
}

const defaultUpdateTimeout = time.Second * 10

func NewSimpleRegistryDiscovery(registerAddr string, timeout time.Duration) *SimpleRegistryDiscovery {
	if timeout == 0 {
		timeout = defaultUpdateTimeout
	}

	d := &SimpleRegistryDiscovery{
		MultiServersDiscovery:NewMultiServerDiscovery(make([]string, 0)),
		registry:registerAddr,
		timeout:timeout,
	}

	return d
}

func (d *SimpleRegistryDiscovery) Update(servers []string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.servers = servers
	d.lastUpdate = time.Now()
	return nil
}

func (d *SimpleRegistryDiscovery) Refresh() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.lastUpdate.Add(d.timeout).After(time.Now()) {
		return nil
	}

	log.Println("rpc registry: refresh servers from registry", d.registry)
	resp, err := http.Get(d.registry)
	if err != nil {
		log.Println("rpc registry refresh err:", err)
		return err
	}

	servers := strings.Split(resp.Header.Get("X-Simplerpc-Servers"), ",")
	d.servers = make([]string, 0, len(servers))
	for _, server := range servers {
		if strings.TrimSpace(server) != "" {
			d.servers = append(d.servers, strings.TrimSpace(server))
		}
	}
	d.lastUpdate = time.Now()
	return nil
}

func (d *SimpleRegistryDiscovery) Get(mode SelectMode) (string, error) {
	if err := d.Refresh(); err != nil {
		return "", err
	}

	return d.MultiServersDiscovery.Get(mode)
}

func (d *SimpleRegistryDiscovery) GetAll() ([]string, error) {
	if err := d.Refresh(); err != nil {
		return nil, err
	}

	return d.MultiServersDiscovery.GetAll()
}
