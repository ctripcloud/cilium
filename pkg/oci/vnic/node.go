// Copyright 2021 Authors of Cilium
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
	"fmt"

	"github.com/cilium/cilium/pkg/defaults"
	"github.com/cilium/cilium/pkg/ipam"
	ipamTypes "github.com/cilium/cilium/pkg/ipam/types"
	"github.com/cilium/cilium/pkg/k8s/apis/cilium.io/v2"
	"github.com/cilium/cilium/pkg/lock"
	"github.com/cilium/cilium/pkg/math"
	// "github.com/cilium/cilium/pkg/oci/utils"
	"github.com/cilium/cilium/pkg/oci/vnic/limits"
	vnicTypes "github.com/cilium/cilium/pkg/oci/vnic/types"

	"github.com/sirupsen/logrus"
)

// The following error constants represent the error conditions for
// CreateInterface without additional context embedded in order to make them
// usable for metrics accounting purposes.
const (
	errUnableToDetermineLimits   = "unable to determine limits"
	errUnableToGetSecurityGroups = "unable to get security groups"
	errUnableToCreateVNIC        = "unable to create VNIC"
	errUnableToAttachVNIC        = "unable to attach VNIC"
	errUnableToFindSubnet        = "unable to find matching subnet"
)

const (
	maxVNICIPCreate = 32
	maxVNICPerNode  = 24
)

type Node struct {
	// node contains the general purpose fields of a node
	node *ipam.Node

	// mutex protects members below this field
	mutex lock.RWMutex

	// vnics is the list of VNICs attached to the node indexed by VNIC ID.
	// Protected by Node.mutex.
	vnics map[string]vnicTypes.VNIC

	// k8sObj is the CiliumNode custom resource representing the node
	k8sObj *v2.CiliumNode

	// manager is the ecs node manager responsible for this node
	manager *InstancesManager

	// instanceID of the node
	instanceID string
}

// UpdatedNode is called when an update to the CiliumNode is received.
func (n *Node) UpdatedNode(obj *v2.CiliumNode) {
	n.mutex.Lock()
	defer n.mutex.Unlock()
	n.k8sObj = obj
}

// PopulateStatusFields fills in the status field of the CiliumNode custom
// resource with OCI specific information
func (n *Node) PopulateStatusFields(resource *v2.CiliumNode) {
	resource.Status.OCI.VNICs = map[string]vnicTypes.VNIC{}

	n.manager.ForeachInstance(
		n.node.InstanceID(),
		func(instanceID, interfaceID string, rev ipamTypes.InterfaceRevision) error {
			v, ok := rev.Resource.(*vnicTypes.VNIC)
			if ok {
				resource.Status.OCI.VNICs[interfaceID] = *v.DeepCopy()
			}
			return nil
		})

	return
}

// CreateInterface creates an additional interface with the instance and
// attaches it to the instance as specified by the CiliumNode. neededAddresses
// of secondary IPs are assigned to the interface up to the maximum number of
// addresses as allowed by the instance.
func (n *Node) CreateInterface(ctx context.Context, allocation *ipam.AllocationAction, scopedLog *logrus.Entry) (int, string, error) {
	log.WithFields(logrus.Fields{
		"node": n.instanceID,
	}).Info("CreateInterface for instance")

	l, limitsAvailable := n.getLimits()
	if !limitsAvailable {
		return 0, errUnableToDetermineLimits, fmt.Errorf(errUnableToDetermineLimits)
	}

	n.mutex.RLock()
	resource := *n.k8sObj
	n.mutex.RUnlock()

	// Must allocate secondary VNIC IPs as needed, up to VNIC instance limit
	toAllocate := math.IntMin(allocation.MaxIPsToAllocate, l.IPv4)
	toAllocate = math.IntMin(maxVNICIPCreate, toAllocate) // in first alloc no more than 10
	// Validate whether request has already been fulfilled in the meantime
	if toAllocate == 0 {
		log.Errorf("toAllocate == 0")
		return 0, "", nil
	}

	ociSpec := resource.Spec.OCI
	bestSubnet := n.manager.FindSubnet(ociSpec.VCNID, ociSpec.AvailabilityDomain, toAllocate, ociSpec.SubnetTags)
	if bestSubnet == nil {
		return 0,
			errUnableToFindSubnet,
			fmt.Errorf(
				"no matching subnet available for interface creation (VCN=%s AZ=%s SubnetTags=%s",
				ociSpec.VCNID,
				ociSpec.AvailabilityDomain,
				ociSpec.SubnetTags,
			)
	}

	// securityGroupIDs, err := n.getSecurityGroupIDs(ctx, ociSpec)
	// if err != nil {
	// 	return 0,
	// 		errUnableToGetSecurityGroups,
	// 		fmt.Errorf("%s %s", errUnableToGetSecurityGroups, err)
	// }

	scopedLog = scopedLog.WithFields(logrus.Fields{
		// "securityGroupIDs": securityGroupIDs,
		"bestSubnetID": bestSubnet.ID,
		"toAllocate":   toAllocate,
	})
	scopedLog.Info("No more IPs available, creating+attaching new VNIC")

	instanceID := n.node.InstanceID()
	n.mutex.Lock()
	defer n.mutex.Unlock()

	attachmentID, err := n.manager.api.AttachNetworkInterface(ctx, instanceID, bestSubnet.ID)
	if err != nil {
		return 0, errUnableToAttachVNIC, fmt.Errorf("%s %s", errUnableToAttachVNIC, err)
	}

	vnic, err := n.manager.api.WaitVNICAttached(ctx, attachmentID)
	if err != nil {
		log.WithFields(logrus.Fields{
			"vnicAttachmentID": attachmentID,
			"instanceID":       instanceID,
		}).Error("Wait for VNIC attach failed")

		// TODO: detach
		return 0, errUnableToAttachVNIC, fmt.Errorf("%s %s", errUnableToAttachVNIC, err)
	}

	if vnic == nil {
		log.WithFields(logrus.Fields{
			"vnicAttachmentID": attachmentID,
			"instanceID":       instanceID,
		}).Error("Wait for VNIC attach failed, returned nil vnic")
		return 0, errUnableToAttachVNIC, fmt.Errorf("returned nil vnic")
	}

	n.vnics[vnic.ID] = *vnic
	scopedLog.WithField(fieldVNICID, vnic.ID).Info("Attached VNIC to instance")

	// Add the information of the created VNIC to the instances manager
	n.manager.UpdateVNIC(instanceID, vnic)
	return toAllocate, "", nil
}

// ResyncInterfacesAndIPs is called to retrieve and VNICs and IPs as known to
// the OCI API and return them
func (n *Node) ResyncInterfacesAndIPs(ctx context.Context, scopedLog *logrus.Entry) (ipamTypes.AllocationMap, error) {
	instanceID := n.node.InstanceID()
	available := ipamTypes.AllocationMap{}

	n.mutex.Lock()
	defer n.mutex.Unlock()
	n.vnics = map[string]vnicTypes.VNIC{}

	n.manager.ForeachInstance(instanceID,
		func(instanceID, interfaceID string, rev ipamTypes.InterfaceRevision) error {
			e, ok := rev.Resource.(*vnicTypes.VNIC)
			if !ok {
				log.Info("rev.Resource.(*vnicTypes.VNIC) failed")
				return nil
			}

			n.vnics[e.ID] = *e
			if e.IsPrimary {
				log.Debug("Skip primary VNIC for OCI ResyncInterfacesAndIPs")
				return nil
			}

			for _, ip := range e.Addresses {
				available[ip] = ipamTypes.AllocationIP{Resource: e.ID}
			}

			return nil
		})

	vnics := len(n.vnics)

	// An OCI instance has at least one VNIC attached, no VNIC found implies instance not found.
	if vnics == 0 {
		scopedLog.Warning("OCI instance not found! Please delete corresponding ciliumnode if instance has already been deleted.")
		return nil, fmt.Errorf("unable to retrieve VNICs")
	}

	return available, nil
}

// PrepareIPAllocation returns the number of VNIC IPs and interfaces that can be
// allocated/created.
func (n *Node) PrepareIPAllocation(scopedLog *logrus.Entry) (*ipam.AllocationAction, error) {
	l, limitsAvailable := n.getLimits()
	if !limitsAvailable {
		return nil, fmt.Errorf("Unable to determine limits")
	}

	a := &ipam.AllocationAction{}

	n.mutex.RLock()
	defer n.mutex.RUnlock()

	for key, e := range n.vnics {
		scopedLog.WithFields(logrus.Fields{
			fieldVNICID: e.ID,
			// "vnicname":  utils.GetVnicDisplayName(&e),
			"ipv4Limit": l.IPv4,
			"allocated": len(e.Addresses),
		}).Info("PrepareIPAllocation: considering VNIC for allocation")

		if e.IsPrimary {
			continue
		}

		availableOnVNIC := math.IntMax(l.IPv4-len(e.Addresses), 0)
		if availableOnVNIC <= 0 {
			continue
		}

		a.AvailableInterfaces++
		scopedLog.WithFields(logrus.Fields{
			"availableOnVNIC": availableOnVNIC,
		}).Info("VNIC has IPs available")

		if subnet := n.manager.GetSubnet(e.Subnet.ID); subnet != nil {
			if subnet.AvailableAddresses > 0 && a.InterfaceID == "" {
				scopedLog.WithFields(logrus.Fields{
					"subnetID":           e.Subnet.ID,
					"availableAddresses": subnet.AvailableAddresses,
				}).Info("Subnet has IPs available")

				a.InterfaceID = key
				a.PoolID = ipamTypes.PoolID(subnet.ID)
				a.AvailableForAllocation = math.IntMin(subnet.AvailableAddresses, availableOnVNIC)
			}
		}
	}

	a.AvailableInterfaces = l.Adapters - len(n.vnics) + a.AvailableInterfaces
	log.WithFields(logrus.Fields{
		"AvailableInterfaces": a.AvailableInterfaces,
	}).Info("PrepareIPAllocation")

	return a, nil
}

// AllocateIPs performs the VNIC allocation operation
func (n *Node) AllocateIPs(ctx context.Context, a *ipam.AllocationAction) error {
	log.WithFields(logrus.Fields{
		"vnicID":     a.InterfaceID,
		"toAllocate": a.AvailableForAllocation,
	}).Info("AllocateIPs for VNIC")

	// NOTE: OCI doesn't support assign/create multiple IP addresses in single request,
	// so we allocate with a loop
	for i := 0; i < a.AvailableForAllocation; i++ {
		_, err := n.manager.api.AssignPrivateIPAddresses(ctx, a.InterfaceID, 1)
		if err != nil {
			return err
		}
	}

	return nil
}

// PrepareIPRelease prepares the release of VNIC IPs.
func (n *Node) PrepareIPRelease(excessIPs int, scopedLog *logrus.Entry) *ipam.ReleaseAction {
	r := &ipam.ReleaseAction{}

	n.mutex.Lock()
	defer n.mutex.Unlock()

	// Iterate over VNICs on this node, select the VNIC with the most
	// addresses available for release
	for key, e := range n.vnics {
		scopedLog.WithFields(logrus.Fields{
			fieldVNICID:    e.ID,
			"numAddresses": len(e.Addresses),
		}).Info("Considering VNIC for IP release")

		if e.IsPrimary {
			log.Info("Skip primary VNIC for OCI PrepareIPRelease")
			continue
		}

		// Count free IP addresses on this VNIC
		ipsOnVNIC := n.k8sObj.Status.OCI.VNICs[e.ID].Addresses
		freeIpsOnVNIC := []string{}
		for _, ip := range ipsOnVNIC {
			_, ipUsed := n.k8sObj.Status.IPAM.Used[ip]
			if !ipUsed {
				freeIpsOnVNIC = append(freeIpsOnVNIC, ip)
			}
		}
		freeOnVNICCount := len(freeIpsOnVNIC)

		if freeOnVNICCount <= 0 {
			continue
		}

		scopedLog.WithFields(logrus.Fields{
			fieldVNICID:       e.ID,
			"excessIPs":       excessIPs,
			"freeOnVNICCount": freeOnVNICCount,
		}).Debug("VNIC has unused IPs that can be released")
		maxReleaseOnVNIC := math.IntMin(freeOnVNICCount, excessIPs)

		r.InterfaceID = key
		r.PoolID = ipamTypes.PoolID(e.VCN.ID)
		r.IPsToRelease = freeIpsOnVNIC[:maxReleaseOnVNIC]
	}

	return r
}

// ReleaseIPs performs the VNIC IP release operation
func (n *Node) ReleaseIPs(ctx context.Context, r *ipam.ReleaseAction) error {
	return n.manager.api.UnassignPrivateIPAddresses(ctx, r.InterfaceID, r.IPsToRelease)
}

// GetMaximumAllocatableIPv4 returns the maximum amount of IPv4 addresses
// that can be allocated to the instance
func (n *Node) GetMaximumAllocatableIPv4() int {
	return 32

	// n.mutex.RLock()
	// defer n.mutex.RUnlock()

	/*
		// Retrieve l for the instance type
		l, limitsAvailable := n.getLimitsLocked()
		if !limitsAvailable {
			return 0
		}

		// Return the maximum amount of IP addresses allocatable on the instance
		// reserve Primary eni
		return (l.Adapters - 1) * l.IPv4
	*/
}

// GetMinimumAllocatableIPv4 returns the minimum amount of IPv4 addresses that
// must be allocated to the instance.
func (n *Node) GetMinimumAllocatableIPv4() int {
	return defaults.IPAMPreAllocation
}

func (n *Node) loggerLocked() *logrus.Entry {
	if n == nil || n.instanceID == "" {
		return log
	}

	return log.WithField("instanceID", n.instanceID)
}

// getLimits returns the interface and IP limits of this node
func (n *Node) getLimits() (ipamTypes.Limits, bool) {
	n.mutex.RLock()
	l, b := n.getLimitsLocked()
	n.mutex.RUnlock()
	return l, b
}

// getLimitsLocked is the same function as getLimits, but assumes the n.mutex
// is read locked.
func (n *Node) getLimitsLocked() (ipamTypes.Limits, bool) {
	return limits.Get(n.k8sObj.Spec.OCI.Shape)
}

func (n *Node) getSecurityGroupIDs(ctx context.Context, eniSpec vnicTypes.OciSpec) ([]string, error) {
	// VNIC must have at least one security group
	// 1. use security group defined by user
	// 2. use security group used by primary VNIC (eth0)

	if len(eniSpec.SecurityGroups) > 0 {
		return eniSpec.SecurityGroups, nil
	}

	if len(eniSpec.SecurityGroupTags) > 0 {
		securityGroups := n.manager.FindSecurityGroupByTags(eniSpec.VCNID, eniSpec.SecurityGroupTags)
		if len(securityGroups) == 0 {
			n.loggerLocked().WithFields(logrus.Fields{
				"vcnID": eniSpec.VCNID,
				"tags":  eniSpec.SecurityGroupTags,
			}).Warn("No security groups match required VCN ID and tags, using primary VNIC's security groups")
		} else {
			groups := make([]string, 0, len(securityGroups))
			for _, secGroup := range securityGroups {
				groups = append(groups, secGroup.ID)
			}
			return groups, nil
		}
	}

	var securityGroups []string

	n.manager.ForeachInstance(n.node.InstanceID(),
		func(instanceID, interfaceID string, rev ipamTypes.InterfaceRevision) error {
			// e, ok := rev.Resource.(*vnicTypes.VNIC)
			// if ok && e.Type == vnicTypes.VNICTypePrimary {
			// 	securityGroups = append(securityGroups, e.SecurityGroupIDs...)
			// }
			return nil
		})

	if len(securityGroups) <= 0 {
		return nil, fmt.Errorf("failed to get security group ids")
	}

	return securityGroups, nil
}

// allocVNICIndex will alloc an monotonically increased index for each VNIC on this instance.
// The index generated the first time this VNIC is created, and stored in VNIC.Tags.
func (n *Node) allocVNICIndex() (int, error) {
	// alloc index for each created VNIC
	used := make([]bool, maxVNICPerNode)
	// TODO: implement this

	// for _, v := range n.vnics {
	// 	index := utils.GetVNICIndexFromTags(v.Tags)
	// 	if index > maxVNICPerNode || index < 0 {
	// 		return 0, fmt.Errorf("VNIC index(%d) is out of range", index)
	// 	}
	// 	used[index] = true
	// }
	// ECS has at least 1 VNIC, 0 is reserved for eth0
	i := 1
	for ; i < maxVNICPerNode; i++ {
		if !used[i] {
			break
		}
	}
	return i, nil
}
