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

package limits

import (
	"context"
	"fmt"
	"strings"

	operatorOption "github.com/cilium/cilium/operator/option"
	ipamTypes "github.com/cilium/cilium/pkg/ipam/types"
	"github.com/cilium/cilium/pkg/lock"
	"github.com/cilium/cilium/pkg/logging"
	"github.com/cilium/cilium/pkg/logging/logfields"
	"github.com/cilium/cilium/pkg/oci/client"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/core"
	"github.com/oracle/oci-go-sdk/v65/resourcesearch"

	"github.com/sirupsen/logrus"
)

var log = logging.DefaultLogger.WithField(logfields.LogSubsys, "oci-vnic-limits")

// limits contains limits for adapter count and addresses. The mappings will be
// updated from agent configuration at bootstrap time.
//
// Source: https://www.alibabacloud.com/help/doc-detail/25378.htm
var limits = struct {
	lock.RWMutex

	m map[string]ipamTypes.Limits
}{
	m: map[string]ipamTypes.Limits{},
}

// Update update the limit map
func Update(limitMap map[string]ipamTypes.Limits) {
	limits.Lock()
	defer limits.Unlock()

	for k, v := range limitMap {
		limits.m[k] = v
	}
}

// Get returns the instance limits of a particular instance type.
func Get(instanceType string) (limit ipamTypes.Limits, ok bool) {
	limits.RLock()
	limit, ok = limits.m[instanceType]
	limits.RUnlock()
	return
}

// UpdateFromAPI updates limits for instance
// Shape list:
// https://docs.oracle.com/en-us/iaas/Content/Compute/References/computeshapes.htm#Compute_Shapes
func UpdateFromAPI(ctx context.Context, client *client.OCIClient) error {
	var err error
	vcnID := operatorOption.Config.OCIVCNID
	if vcnID == "" {
		return fmt.Errorf("OCI VCN ID could not be empty")
		// TODO: https://docs.oracle.com/en-us/iaas/Content/Compute/Tasks/gettingmetadata.htm
		// vcnID, err = metadata.GetVPCID(context.TODO())
		// if err != nil {
		// 	return err
		// }
	}

	log.Info("Searching VCN info from OCI, vcnID %s", vcnID)

	// https://docs.oracle.com/en-us/iaas/api/#/en/search/20180409/ResourceSummary/SearchResources
	req := resourcesearch.SearchResourcesRequest{
		SearchDetails: resourcesearch.FreeTextSearchDetails{
			Text: common.String(vcnID),
		},
	}
	r, err := client.ResourceSearchClient.SearchResources(context.Background(), req)
	if err != nil {
		panic(err)
	}

	if r.Items == nil || len(r.Items) == 0 {
		return fmt.Errorf("empty VCN info in search result")
	}

	compartmentID := r.Items[0].CompartmentId
	client.CompartmentID = *compartmentID
	log.Infof("Listing shapes with compartmentID %s", *compartmentID)

	// List shapes (instance types)
	request := core.ListShapesRequest{
		CompartmentId: compartmentID,
		// ImageId:       imageID,
	}
	// API: https://docs.oracle.com/en-us/iaas/api/#/en/iaas/20160918/Shape/ListShapes
	r2, err := client.ComputeClient.ListShapes(ctx, request)
	if err != nil {
		return err
	}

	if r2.Items == nil || len(r2.Items) == 0 {
		return fmt.Errorf("empty shape list returned by ListShapes")
	}

	limits.Lock()
	defer limits.Unlock()

	skippedShapes := []string{}
	for _, shape := range r2.Items {
		instType := *shape.Shape // name of the shape
		if !strings.HasPrefix(instType, "VM.") {
			skippedShapes = append(skippedShapes, instType)
			continue
		}

		adapterLimit := *shape.MaxVnicAttachments
		ipv4PerAdapter := 32
		ipv6PerAdapter := 32

		limits.m[instType] = ipamTypes.Limits{
			Adapters: adapterLimit,
			IPv4:     ipv4PerAdapter,
			IPv6:     ipv6PerAdapter,
		}
	}

	log.WithFields(logrus.Fields{
		"shapes": skippedShapes,
	}).Info("Skip unsupported shapes")

	if len(limits.m) == 0 {
		return fmt.Errorf("no supported shapes found")
	}

	log.WithFields(logrus.Fields{
		"shapeLimits": limits.m,
	}).Info("Init limits for instance types successful")
	return nil
}
