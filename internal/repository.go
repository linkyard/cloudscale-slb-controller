package cloudscale_slb_controller

import (
	"fmt"
	"sync"
)

type FloatingIpInformation struct {
	FloatingIp       string
	ServerName       string
	ServiceNamespace string
	ServiceName      string
}

type Repository interface {
	Clear()

	GetService(namespace string, name string) bool
	PutService(namespace string, name string)
	DeleteService(namespace string, name string)

	GetFloatingIpForService(namespace string, name string) (FloatingIpInformation, bool)
	GetFloatingIp(ip string) (FloatingIpInformation, bool)
	PutFloatingIp(serviceNamespace string, serviceName string, ip string, serverName string) FloatingIpInformation
	DeleteFloatingIp(ip string)
}

func NewRepository() Repository {
	return &HashMapRepository{
		services:         make(map[string]bool),
		floatingIps:      make(map[string]FloatingIpInformation),
		servicesMutex:    sync.Mutex{},
		floatingIpsMutex: sync.Mutex{},
	}
}

type HashMapRepository struct {
	services    map[string]bool
	floatingIps map[string]FloatingIpInformation

	servicesMutex    sync.Mutex
	floatingIpsMutex sync.Mutex
}

func (repo *HashMapRepository) Clear() {
	repo.servicesMutex.Lock()
	defer repo.servicesMutex.Unlock()
	repo.floatingIpsMutex.Lock()
	defer repo.floatingIpsMutex.Unlock()

	repo.floatingIps = make(map[string]FloatingIpInformation)
	repo.services = make(map[string]bool)
}

func (repo *HashMapRepository) GetService(namespace string, name string) bool {
	repo.servicesMutex.Lock()
	defer repo.servicesMutex.Unlock()

	_, ok := repo.services[fmt.Sprintf("%v/%v", namespace, name)]
	return ok
}

func (repo *HashMapRepository) PutService(namespace string, name string) {
	repo.servicesMutex.Lock()
	defer repo.servicesMutex.Unlock()

	repo.services[fmt.Sprintf("%v/%v", namespace, name)] = true
}

func (repo *HashMapRepository) DeleteService(namespace string, name string) {
	repo.servicesMutex.Lock()
	defer repo.servicesMutex.Unlock()

	delete(repo.services, fmt.Sprintf("%v/%v", namespace, name))
}

func (repo *HashMapRepository) GetFloatingIpForService(namespace string, name string) (FloatingIpInformation, bool) {
	repo.floatingIpsMutex.Lock()
	defer repo.floatingIpsMutex.Unlock()

	for _, fip := range repo.floatingIps {
		if fip.ServiceNamespace == namespace && fip.ServiceName == name {
			return fip, true
		}
	}

	return FloatingIpInformation{}, false
}

func (repo *HashMapRepository) GetFloatingIp(ip string) (FloatingIpInformation, bool) {
	repo.floatingIpsMutex.Lock()
	defer repo.floatingIpsMutex.Unlock()

	floatingIp, ok := repo.floatingIps[ip]
	return floatingIp, ok
}

func (repo *HashMapRepository) PutFloatingIp(serviceNamespace string, serviceName string, ip string, serverName string) FloatingIpInformation {
	repo.floatingIpsMutex.Lock()
	defer repo.floatingIpsMutex.Unlock()

	repo.servicesMutex.Lock()
	defer repo.servicesMutex.Unlock()
	repo.services[fmt.Sprintf("%v/%v", serviceNamespace, serviceName)] = true

	ipInfo := FloatingIpInformation{
		FloatingIp:       ip,
		ServerName:       serverName,
		ServiceNamespace: serviceNamespace,
		ServiceName:      serviceName,
	}
	repo.floatingIps[ip] = ipInfo
	return ipInfo
}

func (repo *HashMapRepository) DeleteFloatingIp(ip string) {
	repo.floatingIpsMutex.Lock()
	defer repo.floatingIpsMutex.Unlock()

	repo.servicesMutex.Lock()
	defer repo.servicesMutex.Unlock()
	floatingIp, ok := repo.floatingIps[ip]
	if ok {
		delete(repo.services, fmt.Sprintf("%v/%v", floatingIp.ServiceNamespace, floatingIp.ServiceName))
	}

	delete(repo.floatingIps, ip)
}
