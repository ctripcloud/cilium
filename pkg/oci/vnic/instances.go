// Copyright 2022 Authors of Cilium
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package vnic

import (
	"context"
	"time"

	"github.com/cilium/cilium/pkg/ipam"
	ipamTypes "github.com/cilium/cilium/pkg/ipam/types"
	"github.com/cilium/cilium/pkg/k8s/apis/cilium.io/v2"
	"github.com/cilium/cilium/pkg/lock"
	"github.com/cilium/cilium/pkg/oci/types"
	vnicTypes "github.com/cilium/cilium/pkg/oci/vnic/types"

	"github.com/sirupsen/logrus"
)

// OCIAPI is the API surface used of the ECS API
type OCIAPI interface {
	ListInstances(ctx context.Context, vcns ipamTypes.VirtualNetworkMap, subnets ipamTypes.SubnetMap) (*ipamTypes.InstanceMap, error)
	ListSubnets(ctx context.Context) (ipamTypes.SubnetMap, error)
	GetVPC(ctx context.Context, vpcID string) (*ipamTypes.VirtualNetwork, error)
	ListVCNs(ctx context.Context) (ipamTypes.VirtualNetworkMap, error)
	GetSecurityGroups(ctx context.Context) (types.SecurityGroupMap, error)
	AttachNetworkInterface(ctx context.Context, instanceID, eniID string) (string, error)
	WaitVNICAttached(ctx context.Context, vnicAttachmentID string) (*vnicTypes.VNIC, error)
	AssignPrivateIPAddresses(ctx context.Context, eniID string, toAllocate int) ([]string, error)
	UnassignPrivateIPAddresses(ctx context.Context, eniID string, addresses []string) error
}

// InstancesManager maintains the list of instances. It must be kept up to date
// by calling resync() regularly.
type InstancesManager struct {
	mutex          lock.RWMutex
	instances      *ipamTypes.InstanceMap
	subnets        ipamTypes.SubnetMap
	vcns           ipamTypes.VirtualNetworkMap
	securityGroups types.SecurityGroupMap
	api            OCIAPI
}

// NewInstancesManager returns a new instances manager
func NewInstancesManager(api OCIAPI) *InstancesManager {
	return &InstancesManager{
		instances: ipamTypes.NewInstanceMap(),
		api:       api,
	}
}

// CreateNode
func (m *InstancesManager) CreateNode(obj *v2.CiliumNode, node *ipam.Node) ipam.NodeOperations {
	return &Node{k8sObj: obj, manager: m, node: node, instanceID: node.InstanceID()}
}

// GetPoolQuota returns the number of available IPs in all IP pools
func (m *InstancesManager) GetPoolQuota() ipamTypes.PoolQuotaMap {
	pool := ipamTypes.PoolQuotaMap{}
	for subnetID, subnet := range m.ListSubnets() {
		pool[ipamTypes.PoolID(subnetID)] = ipamTypes.PoolQuota{
			AvailabilityZone: subnet.AvailabilityZone,
			AvailableIPs:     subnet.AvailableAddresses,
		}
	}
	return pool
}

// Resync fetches the list of ECS instances and subnets and updates the local
// cache in the instanceManager. It returns the time when the resync has
// started or time.Time{} if it did not complete.
func (m *InstancesManager) Resync(ctx context.Context) time.Time {
	resyncStart := time.Now()

	vcns, err := m.api.ListVCNs(ctx)
	if err != nil {
		log.WithError(err).Warning("Unable to synchronize VPC list")
		return time.Time{}
	}

	subnets, err := m.api.ListSubnets(ctx)
	if err != nil {
		log.WithError(err).Warning("Unable to retrieve subnets list")
		return time.Time{}
	}

	// securityGroups, err := m.api.GetSecurityGroups(ctx)
	// if err != nil {
	// 	log.WithError(err).Warning("Unable to retrieve ECS security group list")
	// 	return time.Time{}
	// }

	instances, err := m.api.ListInstances(ctx, vcns, subnets)
	if err != nil {
		log.WithError(err).Warning("Unable to synchronize OCI interface list")
		return time.Time{}
	}

	log.WithFields(logrus.Fields{
		"numInstances": instances.NumInstances(),
		"numVCNs":      len(vcns),
		"numSubnets":   len(subnets),
		// "numSecurityGroups": len(securityGroups),
	}).Info("Synchronize vcn/subnet/instance/vnic information from OCI successful")

	m.mutex.Lock()
	m.vcns = vcns
	m.subnets = subnets
	m.instances = instances
	// m.securityGroups = securityGroups
	m.mutex.Unlock()

	return resyncStart
}

// ListSubnets returns all the tracked subnets
// The returned subnetMap is immutable so it can be safely accessed
func (m *InstancesManager) ListSubnets() ipamTypes.SubnetMap {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	subnetsCopy := make(ipamTypes.SubnetMap)
	for k, v := range m.subnets {
		subnetsCopy[k] = v
	}

	return subnetsCopy
}

// GetSubnet returns subnet by id
func (m *InstancesManager) GetSubnet(id string) *ipamTypes.Subnet {
	m.mutex.RLock()
	defer m.mutex.RUnlock()
	return m.subnets[id]
}

// ForeachInstance will iterate over each instance inside `instances`, and call
// `fn`. This function is read-locked for the entire execution.
func (m *InstancesManager) ForeachInstance(instanceID string, fn ipamTypes.InterfaceIterator) {
	m.mutex.RLock()
	defer m.mutex.RUnlock()
	m.instances.ForeachInterface(instanceID, fn)
}

// UpdateVNIC updates the VNIC definition of an VNIC for a particular instance. If
// the VNIC is already known, the definition is updated, otherwise the VNIC is
// added to the instance.
func (m *InstancesManager) UpdateVNIC(instanceID string, eni *vnicTypes.VNIC) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	eniRevision := ipamTypes.InterfaceRevision{Resource: eni}
	m.instances.Update(instanceID, eniRevision)
}

// FindSubnet returns the subnet with the fewest available addresses, matching vpc, ad and tags
func (m *InstancesManager) FindSubnet(vpc, ad string, toAllocate int, subnetTags ipamTypes.Tags) *ipamTypes.Subnet {
	var bestSubnet *ipamTypes.Subnet

	for _, subnet := range m.ListSubnets() {
		scopedLog := log.WithFields(logrus.Fields{
			"vpcFilter":                  vpc,
			"vpcOfSubnet":                subnet.VirtualNetworkID,
			"availabilityDomainFilter":   ad,
			"availabilityDomainOfSubnet": subnet.AvailabilityZone,
			"numIPstoAllocate":           toAllocate,
			"numIPsAvailableOfSubnet":    subnet.AvailableAddresses,
			"tagsFilter":                 subnetTags,
			"tagsOfSubnet":               subnet.Tags,
		})

		if subnet.VirtualNetworkID != vpc {
			scopedLog.Info("FindSubnet: skip this subnet due to VCN mismatch")
			continue
		}

		if subnet.AvailabilityZone == "" {
			scopedLog.Debug("FindSubnet: skip availability domain filter as this is an OCI regional subnet")
		} else {
			if subnet.AvailabilityZone != ad {
				scopedLog.Info("FindSubnet: skip this subnet due to availability domain mismatch")
				continue
			}
		}

		if subnet.AvailableAddresses < toAllocate {
			scopedLog.Info("FindSubnet: skip this subnet due toAllocate too big")
			continue
		}

		if !subnet.Tags.Match(subnetTags) {
			scopedLog.Info("FindSubnet: skip this subnet due to subnet labels mismatch")
			continue
		}

		if bestSubnet == nil || bestSubnet.AvailableAddresses > subnet.AvailableAddresses {
			bestSubnet = subnet
			scopedLog.Info("FindSubnet: subnet selected as the latest best candidate")
		}
	}

	return bestSubnet
}

// FindSecurityGroupByTags returns the security groups matching VPC ID and all required tags
// The returned security groups slice is immutable so it can be safely accessed
func (m *InstancesManager) FindSecurityGroupByTags(vpcID string, required ipamTypes.Tags) []*types.SecurityGroup {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	securityGroups := []*types.SecurityGroup{}
	for _, securityGroup := range m.securityGroups {
		if securityGroup.VPCID == vpcID && securityGroup.Tags.Match(required) {
			securityGroups = append(securityGroups, securityGroup)
		}
	}

	return securityGroups
}
