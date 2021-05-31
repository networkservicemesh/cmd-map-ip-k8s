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
	"os"
	"os/signal"
	"syscall"

	nested "github.com/antonfisher/nested-logrus-formatter"
	"github.com/kelseyhightower/envconfig"

	"github.com/sirupsen/logrus"

	"github.com/networkservicemesh/cmd-map-ip-k8s/internal/mapipwriter"
	"github.com/networkservicemesh/sdk/pkg/tools/log"
	"github.com/networkservicemesh/sdk/pkg/tools/log/logruslogger"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// Config represents the configuration for cmd-map-ip-k8s application
type Config struct {
	OutputPath string `default:"OutputPath" desc:"Path to writing map of internal to extenrnal ips"`
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

	var eventsCh = make(chan watch.Event, len(list.Items))

	watchClient, err := c.CoreV1().Nodes().Watch(ctx, v1.ListOptions{})
	if err != nil {
		logger.Fatal(err.Error())
	}

	go func() {
		for i := 0; i < len(list.Items); i++ {
			eventsCh <- watch.Event{Type: watch.Added, Object: &list.Items[i]}
		}
		for event := range watchClient.ResultChan() {
			eventsCh <- event
		}
	}()

	go mapWriter.Start(ctx, eventsCh)

	<-ctx.Done()
}
