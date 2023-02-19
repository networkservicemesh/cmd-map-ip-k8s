// Copyright (c) 2023 Cisco and/or its affiliates.
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

package main_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
	"gopkg.in/yaml.v2"

	mainpkg "github.com/networkservicemesh/cmd-map-ip-k8s"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/fake"
	k8stest "k8s.io/client-go/testing"
)

func Test_NodeHasChanged(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreTopFunction("k8s.io/klog/v2.(*loggingT).flushDaemon"))

	var ctx, cancel = context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	var conf = &mainpkg.Config{
		OutputPath: filepath.Join(t.TempDir(), "output.yaml"),
	}

	var client = fake.NewSimpleClientset()
	watcher := watch.NewFake()
	client.PrependWatchReactor("nodes", k8stest.DefaultWatchReactor(watcher, nil))

	var appCh = mainpkg.Start(ctx, conf, client)
	go func() {
		defer watcher.Stop()
		time.Sleep(time.Millisecond * 30)
		watcher.Add(&v1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name: "node-1",
			},
			Status: v1.NodeStatus{
				Addresses: []v1.NodeAddress{
					{
						Type:    v1.NodeExternalIP,
						Address: "1.1.1.1",
					},
					{
						Type:    v1.NodeInternalIP,
						Address: "2.2.2.2",
					},
				},
			},
		})
	}()

	require.Len(t, appCh, 0)

	require.Eventually(t, func() bool {
		return verifyIPmap(
			conf.OutputPath,
			map[string]string{
				"1.1.1.1": "2.2.2.2",
				"2.2.2.2": "1.1.1.1",
			},
		)
	}, time.Second*2, time.Second/10)
}

func Test_ConfigMapLoadedFromStart(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreTopFunction("k8s.io/klog/v2.(*loggingT).flushDaemon"))

	var ctx, cancel = context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	var conf = &mainpkg.Config{
		OutputPath:    filepath.Join(t.TempDir(), "output.yaml"),
		FromConfigMap: "test",
		Namespace:     "nsm",
	}

	var client = fake.NewSimpleClientset()
	_, err := client.CoreV1().ConfigMaps(conf.Namespace).Create(ctx, &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test",
			Namespace: "nsm",
		},
		Data: map[string]string{
			"config.yaml": "1.1.1.1: 2.2.2.2\n2.2.2.2: 1.1.1.1",
		},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	var appCh = mainpkg.Start(ctx, conf, client)

	require.Len(t, appCh, 0)

	require.Eventually(t, func() bool {
		return verifyIPmap(
			conf.OutputPath,
			map[string]string{
				"1.1.1.1": "2.2.2.2",
				"2.2.2.2": "1.1.1.1",
			},
		)
	}, time.Second*2, time.Second/10)
}

func Test_ConfigMapHasChanged(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreTopFunction("k8s.io/klog/v2.(*loggingT).flushDaemon"))

	var ctx, cancel = context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	var conf = &mainpkg.Config{
		OutputPath:    filepath.Join(t.TempDir(), "output.yaml"),
		FromConfigMap: "test",
		Namespace:     "nsm",
	}

	var client = fake.NewSimpleClientset()
	watcher := watch.NewFake()
	client.PrependWatchReactor("configmaps", k8stest.DefaultWatchReactor(watcher, nil))

	var appCh = mainpkg.Start(ctx, conf, client)
	go func() {
		defer watcher.Stop()
		time.Sleep(time.Millisecond * 30)
		watcher.Add(&v1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test",
			},
			Data: map[string]string{
				"config.yaml": "1.1.1.1: 2.2.2.2\n2.2.2.2: 1.1.1.1",
			},
		})
	}()

	require.Len(t, appCh, 0)

	require.Eventually(t, func() bool {
		return verifyIPmap(
			conf.OutputPath,
			map[string]string{
				"1.1.1.1": "2.2.2.2",
				"2.2.2.2": "1.1.1.1",
			},
		)
	}, time.Second*2, time.Second/10)
}

func verifyIPmap(p string, expected map[string]string) bool {
	// #nosec
	b, err := os.ReadFile(p)

	if err != nil {
		return false
	}

	var m = make(map[string]string)

	err = yaml.Unmarshal(b, &m)

	if err != nil {
		return false
	}

	for k, v := range expected {
		if m[k] != v {
			return false
		}
	}

	return true
}
