package cloudscale_slb_controller

import (
	"context"
	"fmt"
	"github.com/cloudscale-ch/cloudscale-go-sdk"
	log "github.com/sirupsen/logrus"
	"golang.org/x/oauth2"
	"strings"
	"sync"
	"time"
)

// an adapter to the cloudscale api / upstream go client to make unit testing easier
type CloudscaleClient interface {
	// returns the ID and name of the server this client is running on
	GetServerId() (string, string, error)
	// returns the ID of the server with the given name
	GetServerIdForNode(ctx context.Context, name string) (string, error)
	// returns the name of the server with the given id
	GetServerNameForServerId(ctx context.Context, id string) (string, error)
	// creates a new floating IP attached to the given server
	CreateFloatingIp(ctx context.Context, serverId string) (FloatingIp, error)
	// returns all floating IPs in the cloudscale.ch account
	ListFloatingIps(ctx context.Context) ([]FloatingIp, error)
	// returns the floating IP with the given IP address
	GetFloatingIp(ctx context.Context, ip string) (FloatingIp, error)
	// updates the floating IP to point to the given server
	UpdateFloatingIp(ctx context.Context, ip string, serverId string) (FloatingIp, error)
	// deletes the floating IP
	DeleteFloatingIp(ctx context.Context, ip string) error
}

type FloatingIp struct {
	Ip       string
	Netmask  string
	ServerId string
}

type AdapterCloudscaleClient struct {
	ipLimit        int
	client         *cloudscale.Client
	metadataClient *cloudscale.MetadataClient
	serverIdToName map[string]string
	serverNameToId map[string]string
	mutex          sync.Mutex
}

func NewCloudscaleClient(token string, ipLimit int) (*AdapterCloudscaleClient, error) {
	tokenSource := oauth2.StaticTokenSource(&oauth2.Token{
		AccessToken: token,
	})
	oauthClient := oauth2.NewClient(context.Background(), tokenSource)
	cloudscaleClient := cloudscale.NewClient(oauthClient)

	return &AdapterCloudscaleClient{
		client:         cloudscaleClient,
		metadataClient: cloudscale.NewMetadataClient(nil),
		ipLimit:        ipLimit,
		serverIdToName: make(map[string]string),
		serverNameToId: make(map[string]string),
		mutex:          sync.Mutex{},
	}, nil
}

func (adapter *AdapterCloudscaleClient) GetServerId() (string, string, error) {
	adapter.mutex.Lock()
	defer adapter.mutex.Unlock()

	serverId, err := adapter.metadataClient.GetServerID()
	if err != nil {
		return serverId, "", err
	}

	// don't call the other function because that one would lock the mutex
	serverName, ok := adapter.serverIdToName[serverId]
	if !ok || serverName == "" {
		log.Debugf("[GetServerId] server name for server with id %v not found; re-reading from cloudscale.ch", serverId)
		err := adapter.recreateServerCache()
		if err != nil {
			return serverId, "", err
		}
	}

	serverName, ok = adapter.serverIdToName[serverId]
	if !ok || serverName == "" {
		log.Warnf("[GetServerId] server with id %v not found on the cloudscale.ch account", serverId)
	}
	return serverId, serverName, nil
}

func (adapter *AdapterCloudscaleClient) GetServerNameForServerId(ctx context.Context, serverId string) (string, error) {
	adapter.mutex.Lock()
	defer adapter.mutex.Unlock()

	serverName, ok := adapter.serverIdToName[serverId]
	if !ok || serverName == "" {
		log.Debugf("[GetServerNameForServerId] server name for server with id %v not found; re-reading from cloudscale.ch", serverId)
		err := adapter.recreateServerCache()
		if err != nil {
			return "", err
		}
	}

	serverName, ok = adapter.serverIdToName[serverId]
	if !ok || serverName == "" {
		log.Warnf("[GetServerNameForServerId] server with id %v not found on the cloudscale.ch account", serverId)
	}
	log.Debugf("[GetServerNameForServerId] returning server name %v for server with id %v", serverName, serverId)
	return serverName, nil
}

func (adapter *AdapterCloudscaleClient) GetServerIdForNode(ctx context.Context, name string) (string, error) {
	adapter.mutex.Lock()
	defer adapter.mutex.Unlock()

	serverId, ok := adapter.serverNameToId[name]
	if !ok || serverId == "" {
		log.Debugf("[GetServerIdForNode] server id for server with name %v not found; re-reading from cloudscale.ch", name)
		err := adapter.recreateServerCache()
		if err != nil {
			return "", err
		}
	}

	serverId, ok = adapter.serverNameToId[name]
	if !ok || serverId == "" {
		log.Warnf("[GetServerIdForNode] server with name %v not found on the cloudscale.ch account", name)
	}
	log.Debugf("[GetServerIdForNode] returning server id %v for server with name %v", serverId, name)
	return serverId, nil
}

func (adapter *AdapterCloudscaleClient) recreateServerCache() error {
	ctx, _ := context.WithTimeout(context.Background(), 60*time.Second)
	servers, err := adapter.client.Servers.List(ctx)
	if err != nil {
		return err
	}
	adapter.serverNameToId = make(map[string]string)
	adapter.serverIdToName = make(map[string]string)

	for _, server := range servers {
		tmpServer := server
		idx := strings.Index(tmpServer.Name, ".")
		internalName := fmt.Sprintf("%v.internal.%v", tmpServer.Name[0:idx], tmpServer.Name[idx + 1:len(tmpServer.Name)])

		log.Debugf("adding server with id %v (%v) to cache", tmpServer.UUID, internalName)
		adapter.serverNameToId[internalName] = tmpServer.UUID
		adapter.serverIdToName[tmpServer.UUID] = internalName
	}
	return nil
}

func (adapter *AdapterCloudscaleClient) CreateFloatingIp(ctx context.Context, serverId string) (FloatingIp, error) {
	log.Debugf("[CreateFloatingIp] creating floating ip on server %v on cloudscale.ch", serverId)
	// obey ip limit
	ips, err := adapter.ListFloatingIps(ctx)
	if err != nil {
		return FloatingIp{}, err
	}
	if len(ips) >= adapter.ipLimit {
		log.Debugf("[CreateFloatingIp] ip limit %v reached", adapter.ipLimit)
		return FloatingIp{}, fmt.Errorf("limit of %v floating ips reached", adapter.ipLimit)
	}

	log.Debug("[CreateFloatingIp] calling cloudscale.ch API")
	floatingIp, err := adapter.client.FloatingIPs.Create(ctx,
		&cloudscale.FloatingIPCreateRequest{
			Server:    serverId,
			IPVersion: 4,
		})

	if err != nil {
		return FloatingIp{}, err
	}

	return FloatingIp{
		ServerId: floatingIp.Server.UUID,
		Ip:       strings.Split(floatingIp.Network, "/")[0],
		Netmask:  strings.Split(floatingIp.Network, "/")[1],
	}, nil
}

func (adapter *AdapterCloudscaleClient) ListFloatingIps(ctx context.Context) ([]FloatingIp, error) {
	log.Debug("[ListFloatingIps] listing floating ips on cloudscale.ch")
	ips, err := adapter.client.FloatingIPs.List(ctx)
	if err != nil {
		return nil, err
	}
	var floatingIps []FloatingIp
	for _, ip := range ips {
		floatingIps = append(floatingIps, FloatingIp{
			ServerId: ip.Server.UUID,
			Ip:       strings.Split(ip.Network, "/")[0],
			Netmask:  strings.Split(ip.Network, "/")[1],
		})
	}

	return floatingIps, nil
}

func (adapter *AdapterCloudscaleClient) GetFloatingIp(ctx context.Context, ip string) (FloatingIp, error) {
	log.Debugf("[GetFloatingIp] loading floating ip %v on cloudscale.ch", ip)
	floatingIP, err := adapter.client.FloatingIPs.Get(ctx, ip)
	if err != nil {
		return FloatingIp{}, err
	}
	return FloatingIp{
		ServerId: floatingIP.Server.UUID,
		Ip:       strings.Split(floatingIP.Network, "/")[0],
		Netmask:  strings.Split(floatingIP.Network, "/")[1],
	}, nil
}

func (adapter *AdapterCloudscaleClient) UpdateFloatingIp(ctx context.Context, ip string, serverId string) (FloatingIp, error) {
	log.Debugf("[UpdateFloatingIp] updating floating ip %v with new server id %v on cloudscale.ch", ip, serverId)
	floatingIP, err := adapter.client.FloatingIPs.Update(ctx, ip, &cloudscale.FloatingIPUpdateRequest{
		Server: serverId,
	})
	if err != nil {
		return FloatingIp{}, err
	}
	return FloatingIp{
		ServerId: floatingIP.Server.UUID,
		Ip:       strings.Split(floatingIP.Network, "/")[0],
		Netmask:  strings.Split(floatingIP.Network, "/")[1],
	}, nil
}

func (adapter *AdapterCloudscaleClient) DeleteFloatingIp(ctx context.Context, ip string) error {
	log.Debugf("[DeleteFloatingIp] deleting floating ip %v on cloudscale.ch", ip)
	return adapter.client.FloatingIPs.Delete(ctx, ip)
}
