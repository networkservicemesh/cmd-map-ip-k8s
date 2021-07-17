// Copyright (c) 2021 Doc.ai and/or its affiliates.
//
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at:
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package mapipwriter provides MapIPWriter struct that can handle events related to v1.Node
package mapipwriter

import (
	"context"
	"io/ioutil"
	"os"
	"path/filepath"

	"github.com/edwarnicke/serialize"
	"gopkg.in/yaml.v2"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/watch"

	"github.com/networkservicemesh/sdk/pkg/tools/log"
)

// MapIPWriter writes IPs from the v1.Node into OutputPath
type MapIPWriter struct {
	OutputPath           string
	exec                 serialize.Executor
	internalToExternalIP map[string]string
}

func (m *MapIPWriter) writeToFile(ctx context.Context) {
	_ = os.MkdirAll(filepath.Dir(m.OutputPath), os.ModePerm)

	bytes, err := yaml.Marshal(m.internalToExternalIP)

	if err != nil {
		log.FromContext(ctx).Errorf("an error during marshaling ips map: %v, err: %v", m.OutputPath, err.Error())
		return
	}

	err = ioutil.WriteFile(m.OutputPath, bytes, os.ModePerm)

	if err != nil {
		log.FromContext(ctx).Errorf("an error during marshaling ips map: %v, err: %v", m.OutputPath, err.Error())
	}
}

// Start starts reading events from the passed channel in the current goroutine
func (m *MapIPWriter) Start(ctx context.Context, eventCh <-chan watch.Event) {
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-eventCh:
			if !ok {
				continue
			}
			node, ok := event.Object.(*v1.Node)
			if !ok {
				continue
			}
			if node == nil {
				continue
			}

			internalIP, externalIP := getAddress(node.Status.Addresses, v1.NodeInternalIP), getAddress(node.Status.Addresses, v1.NodeExternalIP)

			if internalIP == "" && externalIP == "" {
				continue
			}

			if internalIP == "" {
				internalIP = externalIP
			}

			if externalIP == "" {
				externalIP = internalIP
			}

			eventType := event.Type

			m.exec.AsyncExec(func() {
				if m.internalToExternalIP == nil {
					m.internalToExternalIP = map[string]string{}
				}
				switch eventType {
				case watch.Deleted:
					log.FromContext(ctx).Debugf("deleted entry with key: %v", internalIP)
					delete(m.internalToExternalIP, internalIP)

				default:
					m.internalToExternalIP[internalIP] = externalIP
					log.FromContext(ctx).Debugf("added mapping %v --> %v", internalIP, externalIP)
				}
				m.exec.AsyncExec(func() {
					m.writeToFile(ctx)
				})
			})
		}
	}
}

func getAddress(addresses []v1.NodeAddress, addressType v1.NodeAddressType) string {
	for i := 0; i < len(addresses); i++ {
		addr := &addresses[i]
		if addr.Type == addressType {
			return addr.Address
		}
	}

	return ""
}
