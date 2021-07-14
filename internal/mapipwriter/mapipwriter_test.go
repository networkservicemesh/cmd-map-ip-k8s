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

package mapipwriter_test

import (
	"context"
	"io/ioutil"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/networkservicemesh/cmd-map-ip-k8s/internal/mapipwriter"
)

func Test_MapWriter(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	outputFile := filepath.Join(t.TempDir(), "output.yaml")

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*3)
	defer cancel()

	var writer = mapipwriter.MapIPWriter{
		OutputPath: outputFile,
	}

	cleintset := fake.NewSimpleClientset()

	w, err := cleintset.CoreV1().Nodes().Watch(ctx, metav1.ListOptions{})
	require.NoError(t, err)

	defer w.Stop()

	go writer.Start(ctx, w.ResultChan())

	_, err = cleintset.CoreV1().Nodes().Create(ctx, &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "node-controlplane",
		},
		Status: v1.NodeStatus{

			Addresses: []v1.NodeAddress{
				{
					Type:    v1.NodeExternalIP,
					Address: "148.142.120.1",
				},
				{
					Type:    v1.NodeInternalIP,
					Address: "127.0.0.1",
				},
			},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	_, err = cleintset.CoreV1().Nodes().Create(ctx, &v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "node-worker-1",
		},
		Status: v1.NodeStatus{
			Addresses: []v1.NodeAddress{
				{
					Type:    v1.NodeInternalIP,
					Address: "1.1.1.1",
				},
			},
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		// #nosec
		b, readErr := ioutil.ReadFile(outputFile)
		if readErr != nil {
			return false
		}
		s := string(b)
		return strings.Contains(s, "127.0.0.1: 148.142.120.1") && strings.Contains(s, "1.1.1.1: 1.1.1.1")
	}, time.Second, time.Millisecond*100)

	err = cleintset.CoreV1().Nodes().Delete(ctx, "node-worker-1", metav1.DeleteOptions{})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		// #nosec
		b, readErr := ioutil.ReadFile(outputFile)
		if readErr != nil {
			return false
		}
		s := strings.TrimSpace(string(b))
		return s == "127.0.0.1: 148.142.120.1\n148.142.120.1: 127.0.0.1"
	}, time.Second, time.Millisecond*100)
}
