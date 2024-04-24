// Copyright (c) 2021-2022 Doc.ai and/or its affiliates.
//
// Copyright (c) 2023 Cisco and/or its affiliates.
//
// Copyright (c) 2024 OpenInfra Foundation Europe. All rights reserved.
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
	"time"

	nested "github.com/antonfisher/nested-logrus-formatter"
	"github.com/kelseyhightower/envconfig"
	"gopkg.in/yaml.v2"

	"github.com/sirupsen/logrus"

	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/networkservicemesh/cmd-map-ip-k8s/internal/mapipwriter"
	"github.com/networkservicemesh/sdk/pkg/tools/log"
	"github.com/networkservicemesh/sdk/pkg/tools/log/logruslogger"
	"github.com/networkservicemesh/sdk/pkg/tools/opentelemetry"
)

// Config represents the configuration for cmd-map-ip-k8s application
type Config struct {
	OutputPath            string        `default:"external_ips.yaml" desc:"Path to writing map of internal to extenrnal ips" split_words:"true"`
	NodeName              string        `default:"" desc:"The name of node where application is running" split_words:"true"`
	LogLevel              string        `default:"INFO" desc:"Log level" split_words:"true"`
	Namespace             string        `default:"default" desc:"Namespace where is mapip running" split_words:"true"`
	FromConfigMap         string        `default:"" desc:"If it's not empty then gets entries from the configmap" split_words:"true"`
	OpenTelemetryEndpoint string        `default:"otel-collector.observability.svc.cluster.local:4317" desc:"OpenTelemetry Collector Endpoint" split_words:"true"`
	MetricsExportInterval time.Duration `default:"10s" desc:"interval between mertics exports" split_words:"true"`
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
	log.EnableTracing(true)
	logrus.Info("Starting NetworkServiceMesh Client ...")
	logrus.SetFormatter(&nested.Formatter{})
	ctx = log.WithLog(ctx, logruslogger.New(ctx, map[string]interface{}{"cmd": os.Args[:1]}))

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

	level, err := logrus.ParseLevel(conf.LogLevel)
	if err != nil {
		logrus.Fatalf("invalid log level %s", conf.LogLevel)
	}
	logrus.SetLevel(level)

	// ********************************************************************************
	// Configure Open Telemetry
	// ********************************************************************************
	if opentelemetry.IsEnabled() {
		collectorAddress := conf.OpenTelemetryEndpoint
		spanExporter := opentelemetry.InitSpanExporter(ctx, collectorAddress)
		metricExporter := opentelemetry.InitOPTLMetricExporter(ctx, collectorAddress, conf.MetricsExportInterval)
		o := opentelemetry.Init(ctx, spanExporter, metricExporter, "map-ip-k8s")
		defer func() {
			if err = o.Close(); err != nil {
				logger.Error(err.Error())
			}
		}()
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

	<-Start(ctx, conf, c)
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

// Start starts main application
func Start(ctx context.Context, conf *Config, c kubernetes.Interface) <-chan struct{} {
	logger := log.FromContext(ctx)

	var mapWriter = mapipwriter.MapIPWriter{
		OutputPath: conf.OutputPath,
	}

	list, err := c.CoreV1().Nodes().List(ctx, v1.ListOptions{})
	if err != nil {
		logger.Fatal(err.Error())
	}

	var eventsCh = make(chan mapipwriter.Event, 64)

	if conf.FromConfigMap != "" {
		cm, err := c.CoreV1().ConfigMaps(conf.Namespace).Get(ctx, conf.FromConfigMap, v1.GetOptions{})
		if err == nil {
			for _, event := range translateFromConfigmap(watch.Event{
				Type:   watch.Added,
				Object: cm,
			}) {
				eventsCh <- event
			}
		}
	}

	for i := 0; i < len(list.Items); i++ {
		for _, event := range translationFromNode(watch.Event{
			Type:   watch.Added,
			Object: &list.Items[i],
		}) {
			eventsCh <- event
		}
	}

	go mapWriter.Start(ctx, eventsCh)

	go monitorEvents(ctx, eventsCh, func() watch.Interface {
		r, _ := c.CoreV1().Nodes().Watch(ctx, v1.ListOptions{})
		return r
	}, func(e watch.Event) []mapipwriter.Event {
		var result = translationFromNode(e)
		var podEvent = translationFromPodToNode(ctx, e, conf.NodeName)

		if podEvent != nil {
			result = append(result, *podEvent)
		}

		return result
	})

	if conf.FromConfigMap != "" {
		go monitorEvents(ctx, eventsCh, func() watch.Interface {
			r, _ := c.CoreV1().ConfigMaps(conf.FromConfigMap).Watch(ctx, v1.ListOptions{FieldSelector: "meta.name=" + conf.FromConfigMap})
			return r
		}, translateFromConfigmap)
	}
	return ctx.Done()
}

func monitorEvents(ctx context.Context, out chan<- mapipwriter.Event, getWatchFn func() watch.Interface, translateFn func(watch.Event) []mapipwriter.Event) {
	w := getWatchFn()
	defer func() {
		if w != nil {
			w.Stop()
		}
	}()

	for ctx.Err() == nil {
		if w == nil {
			log.FromContext(ctx).Errorf("cant supply watcher")
			time.Sleep(time.Second / 2)
			w = getWatchFn()
			continue
		}

		select {
		case e, ok := <-w.ResultChan():
			if !ok {
				w.Stop()
				w = getWatchFn()
				continue
			}
			events := translateFn(e)
			for _, event := range events {
				out <- event
			}
		case <-ctx.Done():
			return
		}
	}
}

func translateFromConfigmap(e watch.Event) []mapipwriter.Event {
	var res []mapipwriter.Event
	var c = e.Object.(*corev1.ConfigMap)

	for _, v := range c.Data {
		var m map[string]string
		if err := yaml.Unmarshal([]byte(v), &m); err == nil {
			for from, to := range m {
				res = append(res, mapipwriter.Event{
					Type: e.Type,
					Translation: mapipwriter.Translation{
						From: from,
						To:   to,
					},
				})
			}
		}
	}

	return res
}

func translationFromPodToNode(ctx context.Context, e watch.Event, currentNodeName string) *mapipwriter.Event {
	var node = e.Object.(*corev1.Node)

	if node.Name != currentNodeName || e.Type == watch.Deleted {
		return nil
	}

	var result = &mapipwriter.Event{
		Type: watch.Added,
		Translation: mapipwriter.Translation{
			From: getPublicIP(ctx),
		},
	}
	for i := 0; i < len(node.Status.Addresses); i++ {
		if node.Status.Addresses[i].Type == corev1.NodeInternalIP {
			result.To = node.Status.Addresses[i].Address
		}
	}
	for i := 0; i < len(node.Status.Addresses); i++ {
		if node.Status.Addresses[i].Type == corev1.NodeExternalIP {
			result.To = node.Status.Addresses[i].Address
		}
	}

	return result
}

func translationFromNode(e watch.Event) []mapipwriter.Event {
	var result []mapipwriter.Event

	var node = e.Object.(*corev1.Node)

	// map internal ip on itself, in case we don't have an external IP
	for i := 0; i < len(node.Status.Addresses); i++ {
		if node.Status.Addresses[i].Type == corev1.NodeInternalIP {
			result = append(result, mapipwriter.Event{
				Type: e.Type,
				Translation: mapipwriter.Translation{
					From: node.Status.Addresses[i].Address,
					To:   node.Status.Addresses[i].Address,
				},
			})

			result[0].To = node.Status.Addresses[i].Address
		}
	}

	// if we have external IPs, instead map internal IP to external
	for i := 0; i < len(node.Status.Addresses); i++ {
		if node.Status.Addresses[i].Type == corev1.NodeExternalIP {
			for j := 0; j < len(result); j++ {
				result[j].To = node.Status.Addresses[i].Address
			}
			break
		}
	}

	// map external IP to itself, in case we want to send data from external IP
	for i := 0; i < len(node.Status.Addresses); i++ {
		if node.Status.Addresses[i].Type == corev1.NodeExternalIP {
			result = append(result, mapipwriter.Event{
				Type: e.Type,
				Translation: mapipwriter.Translation{
					From: node.Status.Addresses[i].Address,
					To:   node.Status.Addresses[i].Address,
				},
			})
		}
	}

	return result
}
