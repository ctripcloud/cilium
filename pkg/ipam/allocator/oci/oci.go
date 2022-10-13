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

package oci

import (
	"context"
	"fmt"

	operatorMetrics "github.com/cilium/cilium/operator/metrics"
	operatorOption "github.com/cilium/cilium/operator/option"
	"github.com/cilium/cilium/pkg/ipam"
	"github.com/cilium/cilium/pkg/ipam/allocator"
	ipamMetrics "github.com/cilium/cilium/pkg/ipam/metrics"
	"github.com/cilium/cilium/pkg/logging"
	"github.com/cilium/cilium/pkg/logging/logfields"
	pkgClient "github.com/cilium/cilium/pkg/oci/client"
	"github.com/cilium/cilium/pkg/oci/vnic"
	"github.com/cilium/cilium/pkg/oci/vnic/limits"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/core"
	"github.com/oracle/oci-go-sdk/v65/resourcesearch"
)

var log = logging.DefaultLogger.WithField(logfields.LogSubsys, "ipam-allocator-oci")

// AllocatorOCI is an implementation of IPAM allocator interface for OCI VNIC
type AllocatorOCI struct {
	client *pkgClient.OCIClient
}

// Init sets up VNIC limits based on given options
func (a *AllocatorOCI) Init(ctx context.Context) error {
	log.Info("Initializing OCI client ...")

	config := common.DefaultConfigProvider()
	ociClient := pkgClient.OCIClient{}

	// Init virtual network client
	c, err := core.NewVirtualNetworkClientWithConfigurationProvider(config)
	if err != nil {
		panic(err)
	}

	ociClient.VirtualNetworkClient = &c

	// Init compute client
	c2, err := core.NewComputeClientWithConfigurationProvider(config)
	if err != nil {
		panic(err)
	}
	ociClient.ComputeClient = &c2

	// Init resource serach client
	c3, err := resourcesearch.NewResourceSearchClientWithConfigurationProvider(config)
	if err != nil {
		panic(err)
	}
	ociClient.ResourceSearchClient = &c3

	// Init all-in-one client
	a.client = &ociClient

	// Create a request and dependent object(s).
	// resp, err := client.GetVcn(context.Background(), core.GetVcnRequest{VcnId: vpcID})
	// if err != nil {
	// 	return err
	// }

	if err := limits.UpdateFromAPI(ctx, a.client); err != nil {
		return fmt.Errorf("unable to update instance type to adapter limits from OCI API: %w", err)
	}

	log.Info("Init AlloctorOCI successful")

	return nil
}

// Start kicks off VNIC allocation, the initial connection to OCI
// APIs is done in a blocking manner. Provided this is successful, a controller is
// started to manage allocation based on CiliumNode custom resources
func (a *AllocatorOCI) Start(getterUpdater ipam.CiliumNodeGetterUpdater) (allocator.NodeEventHandler, error) {
	log.Info("Starting OCI VNIC allocator...")
	// return nil, nil

	var iMetrics ipam.MetricsAPI
	if operatorOption.Config.EnableMetrics {
		iMetrics = ipamMetrics.NewPrometheusMetrics(operatorMetrics.Namespace, operatorMetrics.Registry)
	} else {
		iMetrics = &ipamMetrics.NoOpMetrics{}
	}

	mgr := vnic.NewInstancesManager(a.client)
	nodeManager, err := ipam.NewNodeManager(mgr, getterUpdater, iMetrics,
		operatorOption.Config.ParallelAllocWorkers,
		false, // operatorOption.Config.OCIReleaseExcessIPs,
	)
	if err != nil {
		return nil, fmt.Errorf("unable to initialize OCI node manager: %w", err)
	}

	if err := nodeManager.Start(context.TODO()); err != nil {
		return nil, err
	}

	return nodeManager, nil
}
