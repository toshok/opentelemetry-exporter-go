// Copyright 2019, Honeycomb, Hound Technology, Inc.
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

// Package honeycomb contains a trace exporter for Honeycomb
package honeycomb

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"

	libhoney "github.com/honeycombio/libhoney-go"
	"go.opentelemetry.io/otel/sdk/export/trace"
)

const (
	defaultDataset = "opentelemetry"
)

// Config defines the basic configuration for the Honeycomb exporter.
type Config struct {
	// APIKey is your Honeycomb authentication token, available from
	// https://ui.honeycomb.io/account. This API key must have permission to
	// send events.
	//
	// Don't have a Honeycomb account? Sign up at https://ui.honeycomb.io/signup.
	APIKey string
}

type exporterConfig struct {
	dataset           string
	serviceName       string
	staticFields      map[string]interface{}
	dynamicFields     map[string]func() interface{}
	apiURL            string
	userAgentAddendum string
	output            libhoney.Output
	onError           func(error)
	debug             bool
}

const (
	expectedStaticFieldCount  = 8
	expectedDynamicFieldCount = 4
)

// ExporterOption is an optional change to the configuration used by the
// NewExporter function.
type ExporterOption func(*exporterConfig) error

func validateField(name string) error {
	if len(name) == 0 {
		return errors.New("field name must not be empty")
	}
	return nil
}

// TargetingDataset specifies the name of the Honeycomb dataset to which the
// exporter will send events.
//
// If not specified, the default dataset name is "opentelemetry."
func TargetingDataset(name string) ExporterOption {
	return func(c *exporterConfig) error {
		if len(name) == 0 {
			return errors.New("dataset name must not be empty")
		}
		c.dataset = name
		return nil
	}
}

// WithServiceName specifies an identifier for your application for use in
// events sent by the exporter. While optional, specifying this name is
// extremely valuable when you instrument multiple services.
//
// If set it will be added to all events as the field "service_name."
func WithServiceName(name string) ExporterOption {
	return func(c *exporterConfig) error {
		if len(name) > 0 {
			c.serviceName = name
		}
		return nil
	}
}

// WithField adds a field with the given name and value to the exporter. Any
// events published by this exporter will include this field.
//
// This function replaces any field registered previously with the same name.
func WithField(name string, value interface{}) ExporterOption {
	return func(c *exporterConfig) error {
		if err := validateField(name); err != nil {
			return err
		}
		if c.staticFields == nil {
			c.staticFields = make(map[string]interface{}, expectedStaticFieldCount)
		}
		c.staticFields[name] = value
		if c.dynamicFields != nil {
			delete(c.dynamicFields, name)
		}
		return nil
	}
}

// WithFields adds a set of fields to the exporter. Any events published by this
// exporter will include fields pairing each name in the given map with its
// corresponding value.
//
// This function replaces any field registered previously with the same name.
func WithFields(m map[string]interface{}) ExporterOption {
	return func(c *exporterConfig) error {
		count := len(m)
		if count == 0 {
			return nil
		}
		if c.staticFields == nil {
			if count < expectedStaticFieldCount {
				count = expectedStaticFieldCount
			}
			c.staticFields = make(map[string]interface{}, count)
		}
		for name, value := range m {
			if err := validateField(name); err != nil {
				return err
			}
			c.staticFields[name] = value
		}
		if c.dynamicFields != nil {
			for name := range m {
				delete(c.dynamicFields, name)
			}
		}
		return nil
	}
}

func validateDynamicField(name string, f func() interface{}) error {
	if len(name) == 0 {
		return errors.New("dynamic field name must not be empty")
	}
	if f == nil {
		return fmt.Errorf("dynamic field %q must have a non-nil function", name)
	}
	return nil
}

// WithDynamicField adds a dynamic field with the given name to the
// exporter. Any events published by this exporter will include a field with the
// given name and a value supplied by invoking the corresponding function.
//
// This function replaces any field registered previously with the same name.
func WithDynamicField(name string, f func() interface{}) ExporterOption {
	return func(c *exporterConfig) error {
		if err := validateDynamicField(name, f); err != nil {
			return err
		}
		if c.dynamicFields == nil {
			c.dynamicFields = make(map[string]func() interface{}, expectedDynamicFieldCount)
		}
		c.dynamicFields[name] = f
		if c.staticFields != nil {
			delete(c.staticFields, name)
		}
		return nil
	}
}

// WithDynamicFields adds a set of dynamic fields to the exporter. Any events
// published by this exporter will include fields pairing each name in the given
// map with a value supplied by invoking the corresponding function.
//
// This function replaces any field registered previously with the same name.
func WithDynamicFields(m map[string]func() interface{}) ExporterOption {
	return func(c *exporterConfig) error {
		count := len(m)
		if count == 0 {
			return nil
		}
		if c.dynamicFields == nil {
			if count < expectedDynamicFieldCount {
				count = expectedDynamicFieldCount
			}
			c.dynamicFields = make(map[string]func() interface{}, count)
		}
		for name, f := range m {
			if err := validateDynamicField(name, f); err != nil {
				return err
			}
			c.dynamicFields[name] = f
		}
		if c.staticFields != nil {
			for name := range m {
				delete(c.staticFields, name)
			}
		}
		return nil
	}
}

// WithAPIURL specifies the URL for the Honeycomb API server to which to send
// events.
//
// If not specified, the default URL is https://api.honeycomb.io/.
func WithAPIURL(url string) ExporterOption {
	return func(c *exporterConfig) error {
		// NB: libhoney.VerifyAPIKey parses this URL to make sure it's valid.
		if len(url) == 0 {
			return errors.New("API URL name must not be empty")
		}
		c.apiURL = url
		return nil
	}
}

// WithUserAgentAddendum specifies additional HTTP user agent-related detail to
// include in HTTP requests issued to send events to the Honeycomb API
// server. This value is appended to the user agent value from the libhoney
// library.
//
// If not specified, the default value is "Honeycomb-OpenTelemetry-exporter." If
// specified as an empty string, no user agent details are appended.
func WithUserAgentAddendum(a string) ExporterOption {
	return func(c *exporterConfig) error {
		c.userAgentAddendum = a
		return nil
	}
}

// CallingOnError specifies a hook function to be called when an error occurs
// sending events to Honeycomb.
//
// If not specified, the default hook logs the errors. Specifying a nil value
// suppresses this default logging behavior.
func CallingOnError(f func(error)) ExporterOption {
	return func(c *exporterConfig) error {
		if f == nil {
			f = func(error) {}
		}
		c.onError = f
		return nil
	}
}

// WithDebugEnabled causes the exporter to emit verbose logging to STDOUT.
//
// If you're having trouble getting the exporter to work, try enabling this
// logging in a development environment to help diagnose the problem.
func WithDebugEnabled() ExporterOption {
	return func(c *exporterConfig) error {
		c.debug = true
		return nil
	}
}

// withHoneycombOutput sets the event output handler on the Honeycomb event
// transmission subsystem.
func withHoneycombOutput(o libhoney.Output) ExporterOption {
	return func(c *exporterConfig) error {
		c.output = o
		return nil
	}
}

// Exporter is an implementation of trace.Exporter that uploads a span to Honeycomb.
type Exporter struct {
	builder *libhoney.Builder
	// serviceName identifies your application. If set it will be added to all
	// events as `service_name`.
	//
	// While optional, setting this field is extremely valuable when you
	// instrument multiple services.
	serviceName string
	// onError is the hook to be called when there is an error occurred when
	// uploading the span data. If no custom hook is set, errors are logged.
	onError func(err error)
}

var _ trace.SpanSyncer = (*Exporter)(nil)

// spanEvent represents an event attached to a specific span.
type spanEvent struct {
	Name     string `json:"name"`
	TraceID  string `json:"trace.trace_id"`
	ParentID string `json:"trace.parent_id,omitempty"`
	SpanType string `json:"meta.span_type"`
}

type spanRefType int64

const (
	spanRefTypeChildOf     spanRefType = 0
	spanRefTypeFollowsFrom spanRefType = 1
)

// span is the format of trace events that Honeycomb accepts.
type span struct {
	TraceID         string  `json:"trace.trace_id"`
	Name            string  `json:"name"`
	ID              string  `json:"trace.span_id"`
	ParentID        string  `json:"trace.parent_id,omitempty"`
	DurationMilli   float64 `json:"duration_ms"`
	Status          string  `json:"response.status_code,omitempty"`
	Error           bool    `json:"error,omitempty"`
	HasRemoteParent bool    `json:"has_remote_parent"`
}

func getHoneycombTraceID(traceID string) string {
	hcTraceUUID, err := uuid.Parse(traceID)
	if err != nil {
		return ""
	}
	return hcTraceUUID.String()
}

func honeycombSpan(s *trace.SpanData) *span {
	sc := s.SpanContext

	hcSpan := &span{
		TraceID:         getHoneycombTraceID(sc.TraceIDString()),
		ID:              sc.SpanIDString(),
		Name:            s.Name,
		HasRemoteParent: s.HasRemoteParent,
	}
	parentID := hex.EncodeToString(s.ParentSpanID[:])
	var initializedParentID [8]byte
	if s.ParentSpanID != sc.SpanID && s.ParentSpanID != initializedParentID {
		hcSpan.ParentID = parentID
	}

	if s, e := s.StartTime, s.EndTime; !s.IsZero() && !e.IsZero() {
		hcSpan.DurationMilli = float64(e.Sub(s)) / float64(time.Millisecond)
	}

	if s.Status != codes.OK {
		hcSpan.Error = true
	}
	return hcSpan
}

// NewExporter returns an implementation of trace.Exporter that uploads spans to Honeycomb.
func NewExporter(config Config, opts ...ExporterOption) (*Exporter, error) {
	// Developer note: bump this with each release
	// TODO: Stamp this via a variable set at link time with a value derived
	// from the current VCS tag.
	const versionStr = "0.2.1"

	if len(config.APIKey) == 0 {
		return nil, errors.New("API key must not be empty")
	}

	econf := exporterConfig{
		userAgentAddendum: "Honeycomb-OpenTelemetry-exporter",
	}
	for _, o := range opts {
		if err := o(&econf); err != nil {
			return nil, err
		}
	}
	if len(econf.dataset) == 0 {
		econf.dataset = defaultDataset
	}

	libhoneyConfig := libhoney.Config{
		WriteKey: config.APIKey,
		Dataset:  econf.dataset,
	}
	if len(econf.apiURL) != 0 {
		libhoneyConfig.APIHost = econf.apiURL
	}
	if userAgent := econf.userAgentAddendum; len(userAgent) != 0 {
		libhoney.UserAgentAddition = userAgent + "/" + versionStr
	}
	if econf.output != nil {
		libhoneyConfig.Output = econf.output
	}
	if econf.debug {
		libhoneyConfig.Logger = &libhoney.DefaultLogger{}
	}

	if err := libhoney.Init(libhoneyConfig); err != nil {
		return nil, err
	}
	builder := libhoney.NewBuilder()

	for name, value := range econf.staticFields {
		builder.AddField(name, value)
	}
	for name, f := range econf.dynamicFields {
		builder.AddDynamicField(name, f)
	}

	onError := econf.onError
	if onError == nil {
		onError = func(err error) {
			log.Printf("Error when sending spans to Honeycomb: %v", err)
		}
	}

	return &Exporter{
		builder:     builder,
		serviceName: econf.serviceName,
		onError:     onError,
	}, nil
}

// ExportSpan exports a SpanData to Honeycomb.
func (e *Exporter) ExportSpan(ctx context.Context, data *trace.SpanData) {
	ev := e.builder.NewEvent()

	if len(e.serviceName) != 0 {
		ev.AddField("service_name", e.serviceName)
	}

	ev.Timestamp = data.StartTime
	ev.Add(honeycombSpan(data))

	// We send these message events as zero-duration spans.
	for _, a := range data.MessageEvents {
		spanEv := e.builder.NewEvent()
		if len(e.serviceName) != 0 {
			spanEv.AddField("service_name", e.serviceName)
		}

		for _, kv := range a.Attributes {
			spanEv.AddField(string(kv.Key), kv.Value.Emit())
		}
		spanEv.Timestamp = a.Time

		spanEv.Add(spanEvent{
			Name:     a.Name,
			TraceID:  getHoneycombTraceID(data.SpanContext.TraceIDString()),
			ParentID: data.SpanContext.SpanIDString(),
			SpanType: "span_event",
		})
		if err := spanEv.Send(); err != nil {
			e.onError(err)
		}
	}

	// link represents a link to a trace and span that lives elsewhere.
	// TraceID and ParentID are used to identify the span with which the trace is associated
	// We are modeling Links for now as child spans rather than properties of the event.
	type link struct {
		TraceID     string      `json:"trace.trace_id"`
		ParentID    string      `json:"trace.parent_id,omitempty"`
		LinkTraceID string      `json:"trace.link.trace_id"`
		LinkSpanID  string      `json:"trace.link.span_id"`
		SpanType    string      `json:"meta.span_type"`
		RefType     spanRefType `json:"ref_type,omitempty"`
	}

	for _, spanLink := range data.Links {
		linkEv := e.builder.NewEvent()
		linkEv.Add(link{
			TraceID:     getHoneycombTraceID(data.SpanContext.TraceIDString()),
			ParentID:    data.SpanContext.SpanIDString(),
			LinkTraceID: getHoneycombTraceID(spanLink.TraceIDString()),
			LinkSpanID:  spanLink.SpanIDString(),
			SpanType:    "link",
			// TODO(akvanhar): properly set the reference type when specs are defined
			// see https://github.com/open-telemetry/opentelemetry-specification/issues/65
			RefType: spanRefTypeChildOf,

			// TODO(akvanhar) add support for link.Attributes
		})
		if err := linkEv.Send(); err != nil {
			e.onError(err)
		}
	}

	for _, kv := range data.Attributes {
		ev.AddField(string(kv.Key), kv.Value.AsInterface())
	}

	ev.AddField("status.code", int32(data.Status))
	// If the status isn't zero, set error to be true.
	if data.Status != 0 {
		ev.AddField("error", true)
	}

	if err := ev.SendPresampled(); err != nil {
		e.onError(err)
	}
}

// Close waits for all in-flight messages to be sent. You should
// call Close() before app termination.
func (e *Exporter) Close() {
	libhoney.Close()
}
