// Copyright The OpenTelemetry Authors
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

package trace // import "go.opentelemetry.io/otel/sdk/trace"

import (
	"encoding/binary"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// Sampler decides whether a trace should be sampled and exported.
type Sampler interface {
	ShouldSample(parameters SamplingParameters) SamplingResult
	Description() string
}

// SamplingParameters contains the values passed to a Sampler.
type SamplingParameters struct {
	ParentContext   trace.SpanContext
	TraceID         trace.TraceID
	Name            string
	HasRemoteParent bool
	Kind            trace.SpanKind
	Attributes      []attribute.KeyValue
	Links           []trace.Link
}

// SamplingDecision indicates whether a span is dropped, recorded and/or sampled.
type SamplingDecision uint8

// Valid sampling decisions
const (
	// Drop will not record the span and all attributes/events will be dropped
	Drop SamplingDecision = iota

	// Record indicates the span's `IsRecording() == true`, but `Sampled` flag
	// *must not* be set
	RecordOnly

	// RecordAndSample has span's `IsRecording() == true` and `Sampled` flag
	// *must* be set
	RecordAndSample
)

// SamplingResult conveys a SamplingDecision, set of Attributes and a Tracestate.
type SamplingResult struct {
	Decision   SamplingDecision
	Attributes []attribute.KeyValue
	Tracestate trace.TraceState
}

type traceIDRatioSampler struct {
	traceIDUpperBound uint64
	description       string
}

func (ts traceIDRatioSampler) ShouldSample(p SamplingParameters) SamplingResult {
	x := binary.BigEndian.Uint64(p.TraceID[0:8]) >> 1
	if x < ts.traceIDUpperBound {
		return SamplingResult{
			Decision:   RecordAndSample,
			Tracestate: p.ParentContext.TraceState(),
		}
	}
	return SamplingResult{
		Decision:   Drop,
		Tracestate: p.ParentContext.TraceState(),
	}
}

func (ts traceIDRatioSampler) Description() string {
	return ts.description
}

// TraceIDRatioBased samples a given fraction of traces. Fractions >= 1 will
// always sample. Fractions < 0 are treated as zero. To respect the
// parent trace's `SampledFlag`, the `TraceIDRatioBased` sampler should be used
// as a delegate of a `Parent` sampler.
//nolint:golint // golint complains about stutter of `trace.TraceIDRatioBased`
func TraceIDRatioBased(fraction float64) Sampler {
	if fraction >= 1 {
		return AlwaysSample()
	}

	if fraction <= 0 {
		fraction = 0
	}

	return &traceIDRatioSampler{
		traceIDUpperBound: uint64(fraction * (1 << 63)),
		description:       fmt.Sprintf("TraceIDRatioBased{%g}", fraction),
	}
}

type alwaysOnSampler struct{}

func (as alwaysOnSampler) ShouldSample(p SamplingParameters) SamplingResult {
	return SamplingResult{
		Decision:   RecordAndSample,
		Tracestate: p.ParentContext.TraceState(),
	}
}

func (as alwaysOnSampler) Description() string {
	return "AlwaysOnSampler"
}

// AlwaysSample returns a Sampler that samples every trace.
// Be careful about using this sampler in a production application with
// significant traffic: a new trace will be started and exported for every
// request.
func AlwaysSample() Sampler {
	return alwaysOnSampler{}
}

type alwaysOffSampler struct{}

func (as alwaysOffSampler) ShouldSample(p SamplingParameters) SamplingResult {
	return SamplingResult{
		Decision:   Drop,
		Tracestate: p.ParentContext.TraceState(),
	}
}

func (as alwaysOffSampler) Description() string {
	return "AlwaysOffSampler"
}

// NeverSample returns a Sampler that samples no traces.
func NeverSample() Sampler {
	return alwaysOffSampler{}
}

// ParentBased returns a composite sampler which behaves differently,
// based on the parent of the span. If the span has no parent,
// the root(Sampler) is used to make sampling decision. If the span has
// a parent, depending on whether the parent is remote and whether it
// is sampled, one of the following samplers will apply:
// - remoteParentSampled(Sampler) (default: AlwaysOn)
// - remoteParentNotSampled(Sampler) (default: AlwaysOff)
// - localParentSampled(Sampler) (default: AlwaysOn)
// - localParentNotSampled(Sampler) (default: AlwaysOff)
func ParentBased(root Sampler, samplers ...ParentBasedSamplerOption) Sampler {
	return parentBased{
		root:   root,
		config: configureSamplersForParentBased(samplers),
	}
}

type parentBased struct {
	root   Sampler
	config config
}

func configureSamplersForParentBased(samplers []ParentBasedSamplerOption) config {
	c := config{
		remoteParentSampled:    AlwaysSample(),
		remoteParentNotSampled: NeverSample(),
		localParentSampled:     AlwaysSample(),
		localParentNotSampled:  NeverSample(),
	}

	for _, so := range samplers {
		so.Apply(&c)
	}

	return c
}

// config is a group of options for parentBased sampler.
type config struct {
	remoteParentSampled, remoteParentNotSampled Sampler
	localParentSampled, localParentNotSampled   Sampler
}

// ParentBasedSamplerOption configures the sampler for a particular sampling case.
type ParentBasedSamplerOption interface {
	Apply(*config)

	// A private method to prevent users implementing the
	// interface and so future additions to it will not
	// violate compatibility.

}

// WithRemoteParentSampled sets the sampler for the case of sampled remote parent.
func WithRemoteParentSampled(s Sampler) ParentBasedSamplerOption {
	return remoteParentSampledOption{s}
}

type remoteParentSampledOption struct {
	s Sampler
}

func (o remoteParentSampledOption) Apply(config *config) {
	config.remoteParentSampled = o.s
}

func (remoteParentSampledOption) private() {}

// WithRemoteParentNotSampled sets the sampler for the case of remote parent
// which is not sampled.
func WithRemoteParentNotSampled(s Sampler) ParentBasedSamplerOption {
	return remoteParentNotSampledOption{s}
}

type remoteParentNotSampledOption struct {
	s Sampler
}

func (o remoteParentNotSampledOption) Apply(config *config) {
	config.remoteParentNotSampled = o.s
}

func (remoteParentNotSampledOption) private() {}

// WithLocalParentSampled sets the sampler for the case of sampled local parent.
func WithLocalParentSampled(s Sampler) ParentBasedSamplerOption {
	return localParentSampledOption{s}
}

type localParentSampledOption struct {
	s Sampler
}

func (o localParentSampledOption) Apply(config *config) {
	config.localParentSampled = o.s
}

func (localParentSampledOption) private() {}

// WithLocalParentNotSampled sets the sampler for the case of local parent
// which is not sampled.
func WithLocalParentNotSampled(s Sampler) ParentBasedSamplerOption {
	return localParentNotSampledOption{s}
}

type localParentNotSampledOption struct {
	s Sampler
}

func (o localParentNotSampledOption) Apply(config *config) {
	config.localParentNotSampled = o.s
}

func (localParentNotSampledOption) private() {}

func (pb parentBased) ShouldSample(p SamplingParameters) SamplingResult {
	if p.ParentContext.IsValid() {
		if p.HasRemoteParent {
			if p.ParentContext.IsSampled() {
				return pb.config.remoteParentSampled.ShouldSample(p)
			}
			return pb.config.remoteParentNotSampled.ShouldSample(p)
		}

		if p.ParentContext.IsSampled() {
			return pb.config.localParentSampled.ShouldSample(p)
		}
		return pb.config.localParentNotSampled.ShouldSample(p)
	}
	return pb.root.ShouldSample(p)
}

func (pb parentBased) Description() string {
	return fmt.Sprintf("ParentBased{root:%s,remoteParentSampled:%s,"+
		"remoteParentNotSampled:%s,localParentSampled:%s,localParentNotSampled:%s}",
		pb.root.Description(),
		pb.config.remoteParentSampled.Description(),
		pb.config.remoteParentNotSampled.Description(),
		pb.config.localParentSampled.Description(),
		pb.config.localParentNotSampled.Description(),
	)
}
