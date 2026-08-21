// SPDX-FileCopyrightText: 2026 Forsway Scandinavia AB
// SPDX-License-Identifier: Apache-2.0

package nfConfigApi

import (
	"encoding/json"
	"testing"
)

// A guaranteed bit rate has to survive the round trip, or it is configured by an operator and
// discarded somewhere between WebConsole and the PCF.
func TestPccQosGuaranteedBitRateRoundTrips(t *testing.T) {
	original := NewPccQos(2, *NewArp(1, PREEMPTCAP_MAY_PREEMPT, PREEMPTVULN_PREEMPTABLE))
	original.SetMaxBrUl("50 Mbps")
	original.SetMaxBrDl("50 Mbps")
	original.SetGbrUl("10 Mbps")
	original.SetGbrDl("20 Mbps")

	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var decoded PccQos
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if got := decoded.GetGbrUl(); got != "10 Mbps" {
		t.Errorf("gbrUl = %q, want %q", got, "10 Mbps")
	}
	if got := decoded.GetGbrDl(); got != "20 Mbps" {
		t.Errorf("gbrDl = %q, want %q", got, "20 Mbps")
	}
	if got := decoded.GetMaxBrUl(); got != "50 Mbps" {
		t.Errorf("maxBrUl = %q, want it unaffected", got)
	}
}

// The fields are optional, exactly like the maximum bit rates beside them, so a non-GBR flow
// carries neither and serialises without them.
func TestPccQosWithoutGuaranteedBitRateOmitsTheFields(t *testing.T) {
	qos := NewPccQos(9, *NewArp(1, PREEMPTCAP_MAY_PREEMPT, PREEMPTVULN_PREEMPTABLE))

	if qos.HasGbrUl() || qos.HasGbrDl() {
		t.Error("a QoS with no guaranteed rate must not report having one")
	}

	encoded, err := json.Marshal(qos)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var asMap map[string]any
	if err := json.Unmarshal(encoded, &asMap); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	for _, absent := range []string{"gbrUl", "gbrDl"} {
		if _, present := asMap[absent]; present {
			t.Errorf("%s must be omitted when unset", absent)
		}
	}
}

// A guaranteed rate present in the JSON must decode into the field itself and not into
// AdditionalProperties, which is where unknown keys go — landing there would mean the model
// does not recognise it.
func TestPccQosGuaranteedBitRateIsNotAnAdditionalProperty(t *testing.T) {
	var decoded PccQos
	if err := json.Unmarshal([]byte(`{"fiveQi":2,"gbrUl":"10 Mbps","arp":{"priorityLevel":1,"preemptCap":"MAY_PREEMPT","preemptVuln":"PREEMPTABLE"}}`), &decoded); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if _, leaked := decoded.AdditionalProperties["gbrUl"]; leaked {
		t.Error("gbrUl leaked into AdditionalProperties; the model does not recognise it")
	}
	if got := decoded.GetGbrUl(); got != "10 Mbps" {
		t.Errorf("gbrUl = %q, want it decoded into the field", got)
	}
}
