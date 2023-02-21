// Copyright (c) 2021-2022 Doc.ai and/or its affiliates.
//
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
	OutputPath            string `default:"external_ips.yaml" desc:"Path to writing map of internal to extenrnal ips"`
	NodeName              string `default:"" desc:"The name of node where application is running"`
	LogLevel              string `default:"INFO" desc:"Log level" split_words:"true"`
	Namespace             string `default:"default" desc:"Namespace where is mapip running" split_words:"true"`
	FromConfigMap         string `default:"" desc:"If it's not empty then gets entries from the configmap" split_words:"true"`
	OpenTelemetryEndpoint string `default:"otel-collector.observability.svc.cluster.local:4317" desc:"OpenTelemetry Collector Endpoint"`
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
		metricExporter := opentelemetry.InitMetricExporter(ctx, collectorAddress)
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

	cm, err := c.CoreV1().ConfigMaps(conf.Namespace).Get(ctx, conf.FromConfigMap, v1.GetOptions{})
	if err == nil {
		for _, event := range translateFromConfigmap(watch.Event{
			Type:   watch.Added,
			Object: cm,
		}) {
			eventsCh <- event
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
	}, translationFromNode)

	if conf.FromConfigMap != "" {
		go monitorEvents(ctx, eventsCh, func() watch.Interface {
			r, _ := c.CoreV1().ConfigMaps(conf.FromConfigMap).Watch(ctx, v1.ListOptions{FieldSelector: "meta.name=" + conf.FromConfigMap})
			return r
		}, translateFromConfigmap)
	}
	return ctx.Done()
}

func monitorEvents(ctx context.Context, out chan<- mapipwriter.Event, getWatchFn func() watch.Interface, translateFn func(watch.Event) []mapipwriter.Event) {
	for ctx.Err() == nil {
		w := getWatchFn()

		if w == nil {
			log.FromContext(ctx).Errorf("cant supply watcher")
			time.Sleep(time.Second / 2)
			continue
		}
		select {
		case e, ok := <-w.ResultChan():
			if !ok {
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

func translationFromNode(e watch.Event) []mapipwriter.Event {
	var result []mapipwriter.Event
	var node = e.Object.(*corev1.Node)

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

	for i := 0; i < len(node.Status.Addresses); i++ {
		if node.Status.Addresses[i].Type == corev1.NodeExternalIP {
			var l = len(result)
			for j := 0; j < l; j++ {
				result[j].To = node.Status.Addresses[i].Address
				result = append(result, mapipwriter.Event{
					Type:        e.Type,
					Translation: result[j].Reverse(),
				})
			}
			break
		}
	}

	if len(result) > 0 && e.Type == watch.Added {
		result = append(result, mapipwriter.Event{
			Type: watch.Added,
			Translation: mapipwriter.Translation{
				From: getPublicIP(context.Background()),
				To:   result[len(result)-1].From,
			},
		})
	}

	return result
}
