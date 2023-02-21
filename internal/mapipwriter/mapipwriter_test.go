// Copyright (c) 2021 Doc.ai and/or its affiliates.
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

package mapipwriter_test

import (
	"context"
	"os"

	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
	"k8s.io/apimachinery/pkg/watch"

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

	var eventCh = make(chan mapipwriter.Event)

	go writer.Start(ctx, eventCh)

	eventCh <- mapipwriter.Event{
		Type: watch.Added,
		Translation: mapipwriter.Translation{
			From: "127.0.0.1",
			To:   "148.142.120.1",
		},
	}

	eventCh <- mapipwriter.Event{
		Type: watch.Added,
		Translation: mapipwriter.Translation{
			From: "1.1.1.1",
			To:   "1.1.1.1",
		},
	}

	require.Eventually(t, func() bool {
		// #nosec
		b, readErr := os.ReadFile(outputFile)
		if readErr != nil {
			return false
		}
		s := string(b)
		return strings.Contains(s, "127.0.0.1: 148.142.120.1") && strings.Contains(s, "1.1.1.1: 1.1.1.1")
	}, time.Second, time.Millisecond*100)

	eventCh <- mapipwriter.Event{
		Type: watch.Deleted,
		Translation: mapipwriter.Translation{
			From: "1.1.1.1",
			To:   "1.1.1.1",
		},
	}

	require.Eventually(t, func() bool {
		// #nosec
		b, readErr := os.ReadFile(outputFile)
		if readErr != nil {
			return false
		}
		s := strings.TrimSpace(string(b))
		return s == "127.0.0.1: 148.142.120.1"
	}, time.Second, time.Millisecond*100)
}
