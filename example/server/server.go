// Copyright 2019, OpenTelemetry Authors
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

package main

import (
	"flag"
	"io"
	"log"
	"net/http"

	"github.com/honeycombio/opentelemetry-exporter-go/honeycomb"
	"go.opentelemetry.io/api/distributedcontext"
	"go.opentelemetry.io/api/key"
	apitrace "go.opentelemetry.io/api/trace"

	"go.opentelemetry.io/plugin/httptrace"
	"go.opentelemetry.io/sdk/trace"
)

func main() {
	trace.Register()

	apikey := flag.String("apikey", "", "Your Honeycomb API Key")
	dataset := flag.String("dataset", "opentelemetry", "Your Honeycomb dataset")
	flag.Parse()

	exporter, err := honeycomb.NewExporter(honeycomb.Config{
		ApiKey:      *apikey,
		Dataset:     *dataset,
		Debug:       true,
		ServiceName: "opentelemetry-server",
	})
	if err != nil {
		log.Fatal(err)
	}

	defer exporter.Close()
	exporter.RegisterSimpleSpanProcessor()

	helloHandler := func(w http.ResponseWriter, req *http.Request) {
		attrs, tags, spanCtx := httptrace.Extract(req.Context(), req)

		req = req.WithContext(distributedcontext.WithMap(req.Context(), distributedcontext.NewMap(distributedcontext.MapUpdate{
			MultiKV: tags,
		})))

		ctx, span := apitrace.GlobalTracer().Start(
			req.Context(),
			"hello",
			apitrace.WithAttributes(attrs...),
			apitrace.ChildOf(spanCtx),
		)
		defer span.End()

		span.SetAttribute(key.New("ex.com/another").String("yes"))
		span.AddEvent(ctx, "handling this...", key.New("request-handled").Int(100))

		_, _ = io.WriteString(w, "Hello, world!\n")
	}

	http.HandleFunc("/hello", helloHandler)
	err = http.ListenAndServe(":7777", nil)
	if err != nil {
		log.Fatal(err)
	}
}
