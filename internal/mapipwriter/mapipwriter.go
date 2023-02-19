// Copyright (c) 2021 Doc.ai and/or its affiliates.
//
// Copyright (c) 2023 Cisco goimports -w -local github.com/networkservicemesh and/or its affiliates.
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
	"fmt"
	"os"
	"path/filepath"

	"github.com/edwarnicke/serialize"
	"gopkg.in/yaml.v2"
	"k8s.io/apimachinery/pkg/watch"

	"github.com/networkservicemesh/sdk/pkg/tools/log"
)

// Translation represents translation of ip addrses
type Translation struct {
	From, To string
}

// Event represents event for the mapipwriter
type Event struct {
	Translation
	Type watch.EventType
}

func (e *Translation) String() string {
	return fmt.Sprintf("%v->%v", e.From, e.To)
}

// Reverse creates a new Translation with swapped From/To fields
func (e *Translation) Reverse() Translation {
	return Translation{
		From: e.To,
		To:   e.From,
	}
}

// MapIPWriter writes IPs from the v1.Node into OutputPath
type MapIPWriter struct {
	OutputPath           string
	exec                 serialize.Executor
	internalToExternalIP map[Translation]struct{} //TODO: use orderedmap
}

func (m *MapIPWriter) writeToFile(ctx context.Context) {
	_ = os.MkdirAll(filepath.Dir(m.OutputPath), os.ModePerm)

	var outmap = make(map[string]string)

	for translation := range m.internalToExternalIP {
		outmap[translation.From] = translation.To
	}

	bytes, err := yaml.Marshal(outmap)

	if err != nil {
		log.FromContext(ctx).Errorf("an error during marshaling ips map: %v, err: %v", m.OutputPath, err.Error())
		return
	}

	err = os.WriteFile(m.OutputPath, bytes, os.ModePerm)

	if err != nil {
		log.FromContext(ctx).Errorf("an error during marshaling ips map: %v, err: %v", m.OutputPath, err.Error())
	}
}

// Start starts reading events from the passed channel in the current goroutine
func (m *MapIPWriter) Start(ctx context.Context, eventCh <-chan Event) {
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-eventCh:
			if !ok {
				continue
			}
			m.exec.AsyncExec(func() {
				if m.internalToExternalIP == nil {
					m.internalToExternalIP = make(map[Translation]struct{})
				}
				switch event.Type {
				case watch.Deleted:
					log.FromContext(ctx).Debugf("deleted entry: %v", event.String())
					delete(m.internalToExternalIP, event.Translation)

				default:
					m.internalToExternalIP[event.Translation] = struct{}{}
					log.FromContext(ctx).Debugf("added entry: %v", event.String())
				}
				m.exec.AsyncExec(func() {
					m.writeToFile(ctx)
				})
			})
		}
	}
}
