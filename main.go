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

package main

import (
	"context"
	"net"
	"os"
	"os/signal"
	"syscall"

	nested "github.com/antonfisher/nested-logrus-formatter"
	"github.com/kelseyhightower/envconfig"

	"github.com/sirupsen/logrus"

	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/networkservicemesh/cmd-map-ip-k8s/internal/mapipwriter"
	"github.com/networkservicemesh/sdk/pkg/tools/log"
	"github.com/networkservicemesh/sdk/pkg/tools/log/logruslogger"
)

// Config represents the configuration for cmd-map-ip-k8s application
type Config struct {
	OutputPath string `default:"external_ips.yaml" desc:"Path to writing map of internal to extenrnal ips"`
	NodeName   string `default:"" desc:"The name of node where application is running"`
}

func main() {
	// ********************************************************************************
	// Configure signal handling context
	// ********************************************************************************
	ctx, cancel := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		// More Linux signals here
		syscall.SIGHUP,
		syscall.SIGTERM,
		syscall.SIGQUIT,
	)
	defer cancel()

	// ********************************************************************************
	// Setup logger
	// ********************************************************************************
	logrus.Info("Starting NetworkServiceMesh Client ...")
	logrus.SetFormatter(&nested.Formatter{})
	ctx = log.WithFields(ctx, map[string]interface{}{"cmd": os.Args[:1]})
	ctx = log.WithLog(ctx, logruslogger.New(ctx))

	logger := log.FromContext(ctx)

	// ********************************************************************************
	// Get config from environment
	// ********************************************************************************
	conf := &Config{}
	if err := envconfig.Usage("nsm", conf); err != nil {
		logger.Fatal(err)
	}
	if err := envconfig.Process("nsm", conf); err != nil {
		logger.Fatalf("error processing rootConf from env: %+v", err)
	}

	// ********************************************************************************
	// Create client-go
	// ********************************************************************************
	kubeConfig, err := rest.InClusterConfig()
	if err != nil {
		logger.Fatalf("can't get Kubernetes config. Are you running this app inside Kubernetes pod")
	}
	c, err := kubernetes.NewForConfig(kubeConfig)
	if err != nil {
		logger.Fatal(err.Error())
	}

	// ********************************************************************************
	// Initialize goroutines for writing ips map
	// ********************************************************************************
	var mapWriter = mapipwriter.MapIPWriter{
		OutputPath: conf.OutputPath,
	}

	list, err := c.CoreV1().Nodes().List(context.Background(), v1.ListOptions{})
	if err != nil {
		logger.Fatal(err.Error())
	}

	var eventsCh = make(chan watch.Event, len(list.Items)+1) // 1 is mapping from POD IP to External IP

	for i := 0; i < len(list.Items); i++ {
		eventsCh <- watch.Event{Type: watch.Added, Object: &list.Items[i]}
	}

	eventsCh <- createPODIPMappingEvent(ctx, conf.NodeName, list.Items)

	watchClient, err := c.CoreV1().Nodes().Watch(ctx, v1.ListOptions{})

	if err != nil {
		logger.Fatal(err.Error())
	}

	go func() {
		for event := range watchClient.ResultChan() {
			eventsCh <- event
		}
	}()

	go mapWriter.Start(ctx, eventsCh)

	<-ctx.Done()
}

func getPublicIP(ctx context.Context) string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		log.FromContext(ctx).Errorf("InterfaceAddrs: %v", err.Error())
		return ""
	}
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ip := ipnet.IP.To4(); ip != nil {
				return ip.String()
			}
			if ip := ipnet.IP.To16(); ip != nil {
				return ip.String()
			}
		}
	}
	log.FromContext(ctx).Warn("not found public ip")
	return ""
}

func createPODIPMappingEvent(ctx context.Context, podNodeName string, nodes []corev1.Node) watch.Event {
	podIP := getPublicIP(ctx)

	var targetNode *corev1.Node

	for i := 0; i < len(nodes); i++ {
		if nodes[i].Name == podNodeName {
			targetNode = &nodes[i]
		}
	}

	if targetNode == nil {
		return watch.Event{}
	}

	var cloneNode = *targetNode
	var isExternalIPExist bool
	var internalIP string

	for i := 0; i < len(cloneNode.Status.Addresses); i++ {
		if cloneNode.Status.Addresses[i].Type == corev1.NodeInternalIP {
			internalIP = cloneNode.Status.Addresses[i].Address
			cloneNode.Status.Addresses[i].Address = podIP
			break
		}
		if cloneNode.Status.Addresses[i].Type == corev1.NodeExternalIP {
			isExternalIPExist = true
			break
		}
	}

	if !isExternalIPExist {
		cloneNode.Status.Addresses = append(cloneNode.Status.Addresses, corev1.NodeAddress{
			Address: internalIP,
			Type:    corev1.NodeExternalIP,
		})
	}

	return watch.Event{Type: watch.Added, Object: &cloneNode}
}
