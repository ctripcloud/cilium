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

package types

import (
	"github.com/cilium/cilium/pkg/ipam/types"
)

// OciSpec is the OCI specification of a node. This specification is considered
// by the cilium-operator to act as an IPAM operator and makes VNIC IPs available
// via the IPAMSpec section.
//
// The VNIC specification can either be provided explicitly by the user or the
// cilium-agent running on the node can be instructed to create the CiliumNode
// custom resource along with an VNIC specification when the node registers
// itself to the Kubernetes cluster.
type OciSpec struct {
	// Shape is the ECS instance type, e.g. "ecs.g6.2xlarge"
	//
	// +kubebuilder:validation:Optional
	Shape string `json:"shape,omitempty"`

	// SecurityGroups is the list of security groups to attach to any VNIC
	// that is created and attached to the instance.
	//
	// +kubebuilder:validation:Optional
	SecurityGroups []string `json:"security-groups,omitempty"`

	// SecurityGroupTags is the list of tags to use when evaluating which
	// security groups to use for the VNIC.
	//
	// +kubebuilder:validation:Optional
	SecurityGroupTags map[string]string `json:"security-group-tags,omitempty"`

	// SubnetTags is the list of tags to use when evaluating what AWS
	// subnets to use for ENI and IP allocation.
	//
	// +kubebuilder:validation:Optional
	SubnetTags map[string]string `json:"subnet-tags,omitempty"`

	// VCNID is the VCN ID to use when allocating VNICs.
	//
	// +kubebuilder:validation:Optional
	VCNID string `json:"vcn-id,omitempty"`

	// AvailabilityZone is the availability zone to use when allocating
	// VNICs.
	//
	// +kubebuilder:validation:Optional
	AvailabilityDomain string `json:"availability-domain,omitempty"`

	// CIDRBlock is vcn ipv4 CIDR
	//
	// +kubebuilder:validation:Optional
	// CIDRBlock string `json:"cidr-block,omitempty"`
}

const (
	// VNICTypePrimary is the type for VNIC
	VNICTypePrimary string = "Primary"
	// VNICTypeSecondary is the type for VNIC
	VNICTypeSecondary string = "Secondary"
)

// VNIC represents an OCI virtual NIC
type VNIC struct {
	// ID is the VNIC id
	//
	// +optional
	ID string `json:"id,omitempty"`

	// TODO: enable this field to faciliate debugging
	// DisplayName is the VNIC display name, not unique and may change
	//
	// +optional
	// DisplayName string `json:"display-name,omitempty"`

	// PrimaryIP is the primary IP on VNIC
	//
	// +optional
	PrimaryIP string `json:"primary-ip,omitempty"`

	// MAC is the mac address of the VNIC
	//
	// +optional
	MAC string `json:"mac,omitempty"`

	// AvailabilityDomain is the availability domain of the ENI
	//
	// +optional
	AvailabilityDomain string `json:"availability-domain,omitempty"`

	// Description is the description field of the VNIC
	//
	// +optional
	Description string `json:"description,omitempty"`

	// VCN is the vcn to which the VNIC belongs
	//
	// +optional
	VCN OciVCN `json:"vcn,omitempty"`

	// Subnet is the subnet the VNIC is associated with
	//
	// +optional
	Subnet OciSubnet `json:"subnet,omitempty"`

	// Addresses is the list of all secondary IPs associated with the VNIC
	//
	// +optional
	Addresses []string `json:"addresses,omitempty"`

	// SecurityGroups are the security groups associated with the VNIC
	SecurityGroups []string `json:"security-groups,omitempty"`

	// Whether the VNIC is the primary VNIC (the VNIC that is automatically created
	// and attached during instance launch).
	IsPrimary bool `json:"is-primary,omitempty"`
}

// InterfaceID returns the identifier of the interface
func (e *VNIC) InterfaceID() string {
	return e.ID
}

// ForeachAddress iterates over all addresses and calls fn
func (e *VNIC) ForeachAddress(id string, fn types.AddressIterator) error {
	for _, address := range e.Addresses {
		// if address.Primary {
		// 	continue
		// }
		if err := fn(id, e.ID, address, "", address); err != nil {
			return err
		}
	}

	return nil
}

// OciStatus is the status of VNIC addressing of the node
type OciStatus struct {
	// VNICs is the list of VNICs on the node
	//
	// +optional
	VNICs map[string]VNIC `json:"vnics,omitempty"`
}

// OciSubnet stores information regarding an OCI subnet
type OciSubnet struct {
	// ID is the ID of the subnet
	ID string `json:"id,omitempty"`

	// CIDR is the CIDR range associated with the subnet
	CIDR string `json:"cidr,omitempty"`
}

// OciVCN stores information regarding an OCI VCN
type OciVCN struct {
	/// ID is the ID of a VCN
	ID string `json:"id,omitempty"`

	// Deprecated by OCI: PrimaryCIDR is the primary CIDR of the VCN
	// PrimaryCIDR string `json:"primary-cidr,omitempty"`

	// CidrBlocks is the list of CIDR blocks associated with the VCN
	CidrBlocks []string `json:"cidr-blocks,omitempty"`
}
