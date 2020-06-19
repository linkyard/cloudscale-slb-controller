package cloudscale_slb_controller

import (
	"context"
	"errors"
	"github.com/cloudscale-ch/cloudscale-go-sdk"
	"github.com/google/uuid"
	"math/rand"
	"strconv"
)

type fakeCloudscaleClient struct {
	ipPrefix    string
	count       int
	serverId    string
	floatingIps []FloatingIp
	chaosChance float64
}

func newFakeCloudscaleClient(chaosChance float64) *fakeCloudscaleClient {
	return &fakeCloudscaleClient{
		ipPrefix:    "10.0.0.",
		count:       -1,
		serverId:    uuid.New().String(),
		floatingIps: make([]FloatingIp, 0),
		chaosChance: chaosChance,
	}
}

func (c *fakeCloudscaleClient) GetServerIdForNode(ctx context.Context, name string) (string, error) {
	if c.chaosChance != 0 {
		if rand.Float64() < c.chaosChance {
			return "", errors.New("call failed due to chaos cloudscaleClient:GetServerIdForNode")
		}
	}
	return c.serverId, nil
}

func (c *fakeCloudscaleClient) GetServerNameForServerId(ctx context.Context, id string) (string, error) {
	if c.chaosChance != 0 {
		if rand.Float64() < c.chaosChance {
			return "", errors.New("call failed due to chaos cloudscaleClient:GetServerNameForServerId")
		}
	}
	return c.serverId, nil
}

func (c *fakeCloudscaleClient) GetServerId() (string, string, error) {
	if c.chaosChance != 0 {
		if rand.Float64() < c.chaosChance {
			return "", "", errors.New("call failed due to chaos cloudscaleClient:GetServerId")
		}
	}
	return c.serverId, c.serverId, nil
}

func (c *fakeCloudscaleClient) CreateFloatingIp(ctx context.Context, serverId string) (FloatingIp, error) {
	if c.chaosChance != 0 {
		if rand.Float64() < c.chaosChance {
			return FloatingIp{}, errors.New("call failed due to chaos cloudscaleClient:CreateFloatingIp")
		}
	}
	c.count = c.count + 1
	floatingIp := FloatingIp{
		ServerId: serverId,
		Ip:       c.ipPrefix + strconv.Itoa(c.count),
		Netmask:  "32",
	}
	c.floatingIps = append(c.floatingIps, floatingIp)
	return floatingIp, nil
}

func (c *fakeCloudscaleClient) ListFloatingIps(ctx context.Context) ([]FloatingIp, error) {
	if c.chaosChance != 0 {
		if rand.Float64() < c.chaosChance {
			return make([]FloatingIp, 0), errors.New("call failed due to chaos cloudscaleClient:ListFloatingIps")
		}
	}
	return c.floatingIps, nil
}

func (c *fakeCloudscaleClient) GetFloatingIp(ctx context.Context, ip string) (FloatingIp, error) {
	if c.chaosChance != 0 {
		if rand.Float64() < c.chaosChance {
			return FloatingIp{}, errors.New("call failed due to chaos cloudscaleClient:GetFloatingIp")
		}
	}
	for _, floatingIp := range c.floatingIps {
		if floatingIp.Ip == ip {
			return floatingIp, nil
		}
	}

	return FloatingIp{}, &cloudscale.ErrorResponse{
		StatusCode: 404,
		Message:    map[string]string{"error": "FloatingIP not found"},
	}
}

func (c *fakeCloudscaleClient) UpdateFloatingIp(ctx context.Context, ip string, serverId string) (FloatingIp, error) {
	if c.chaosChance != 0 {
		if rand.Float64() < c.chaosChance {
			return FloatingIp{}, errors.New("call failed due to chaos cloudscaleClient:UpdateFloatingIp")
		}
	}
	idx := -1
	for i, floatingIp := range c.floatingIps {
		if floatingIp.Ip == ip {
			idx = i
			break
		}
	}

	if idx != -1 {
		newFloatingIp := FloatingIp{
			ServerId: serverId,
			Ip:       c.floatingIps[idx].Ip,
			Netmask:  c.floatingIps[idx].Netmask,
		}
		c.floatingIps[idx] = newFloatingIp
		return newFloatingIp, nil
	}

	return FloatingIp{}, &cloudscale.ErrorResponse{
		StatusCode: 404,
		Message:    map[string]string{"error": "FloatingIP not found"},
	}
}

func (c *fakeCloudscaleClient) DeleteFloatingIp(ctx context.Context, ip string) error {
	if c.chaosChance != 0 {
		if rand.Float64() < c.chaosChance {
			return errors.New("call failed due to chaos cloudscaleClient:DeleteFloatingIp")
		}
	}
	idx := -1
	for i, floatingIp := range c.floatingIps {
		if floatingIp.Ip == ip {
			idx = i
			break
		}
	}
	if idx != -1 {
		c.floatingIps = append(c.floatingIps[:idx], c.floatingIps[idx+1:]...)
		return nil
	}

	return &cloudscale.ErrorResponse{
		StatusCode: 404,
		Message:    map[string]string{"error": "FloatingIP not found"},
	}
}
