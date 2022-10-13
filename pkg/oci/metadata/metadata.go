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
package metadata

import (
	"fmt"
	"io"
	"net/http"
)

const (
	metadataBaseURL string = "http://169.254.169.254/opc/v2/"
)

func newClient() (*http.Client, error) {
	c := http.Client{}
	return &c, nil

	// config := common.DefaultConfigProvider()
	// c, err := core.NewComputeManagementClientWithConfigurationProvider(config)
	// if err != nil {
	// 	return nil, err
	// }

	// return &c, nil
}

func getMetadata(client *http.Client, path string) (string, error) {
	url := metadataBaseURL + path
	req, err := http.NewRequest("GET", url, nil)
	req.Header.Add("Authorization", "Bearer Oracle")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("HTTP request failed: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("metadata service returned status code: %v", resp.StatusCode)
	}

	defer resp.Body.Close()
	value, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("unable to read response for OCI metadata %q: %w", path, err)
	}

	return string(value), err
}

// GetInstanceMetadata returns required OCI metadata
func GetInstanceMetadata() (instanceID, instanceShape, availabilityDomain, vcnID string, err error) {
	client, err := newClient()
	if err != nil {
		return
	}

	instanceID, err = getMetadata(client, "instance/id")
	if err != nil {
		return
	}

	instanceShape, err = getMetadata(client, "instance/shape")
	if err != nil {
		return
	}

	availabilityDomain, err = getMetadata(client, "instance/availabilityDomain")
	if err != nil {
		return
	}

	// TODO: hard code to get the mac of the first VNIC
	// eth0MAC, err := getMetadata(client, "vnics/0/macAddr")
	// if err != nil {
	// 	return
	// }

	// TODO: get VCN info with VNIC info?

	// vpcIDPath := fmt.Sprintf("network/interfaces/macs/%s/vpc-id", eth0MAC)
	// vcnID, err = getMetadata(client, vpcIDPath)
	// if err != nil {
	// 	return
	// }

	return
}
