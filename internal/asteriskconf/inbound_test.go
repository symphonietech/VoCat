package asteriskconf

import (
	"strings"
	"testing"
)

func extensions(names ...string) []Extension {
	list := make([]Extension, 0, len(names))
	for _, name := range names {
		list = append(list, Extension{Name: name, Password: "correct-horse-battery", MaxContacts: 2})
	}
	return list
}

// The per-number mode needs no mapping table: the dialled number is the
// request URI, so the extension name is the map.
func TestRenderInboundPerNumberRingsTheMatchingExtension(t *testing.T) {
	plan := InboundPlan{Mode: InboundPerNumber, RingSeconds: 30}
	rendered, err := RenderInbound(plan, extensions("13105557777", "12125551234"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"exten => 13105557777,1,Dial(PJSIP/13105557777,30)\n",
		"exten => 12125551234,1,Dial(PJSIP/12125551234,30)\n",
		// Which form the carrier sends is its choice, not something the
		// operator should have to guess at.
		"exten => +13105557777,1,Dial(PJSIP/13105557777,30)\n",
		"exten => +12125551234,1,Dial(PJSIP/12125551234,30)\n",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("missing %q:\n%s", want, rendered)
		}
	}
	// A DID with no extension of its own gets a truthful cause rather than a
	// silent drop.
	if !strings.Contains(rendered, "same => n,Hangup(1)\n") {
		t.Errorf("no catch-all for an unmatched number:\n%s", rendered)
	}
}

// Ring-all ignores what was dialled, which is the whole point of it.
func TestRenderInboundRingAllRingsEverythingAtOnce(t *testing.T) {
	rendered, err := RenderInbound(
		InboundPlan{Mode: InboundRingAll, RingSeconds: 25},
		extensions("1001", "1002"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered, "exten => _.,1,Dial(PJSIP/1001&PJSIP/1002,25)\n") {
		t.Fatalf("wrong ring-all line:\n%s", rendered)
	}
	// An explicit group wins over "everything".
	rendered, err = RenderInbound(
		InboundPlan{Mode: InboundRingAll, RingSeconds: 25, Extensions: []string{"1002"}},
		extensions("1001", "1002"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered, "Dial(PJSIP/1002,25)") || strings.Contains(rendered, "1001") {
		t.Fatalf("the explicit group was not used:\n%s", rendered)
	}
}

// Dial returns when an answered call ends, not only when it fails. Without
// the DIALSTATUS check the next handset would start ringing the moment the
// first one hung up.
func TestRenderInboundHuntStopsOnAnAnswer(t *testing.T) {
	rendered, err := RenderInbound(
		InboundPlan{Mode: InboundHunt, RingSeconds: 30, HuntSeconds: 15,
			Extensions: []string{"1001", "1002", "1003"}},
		extensions("1001", "1002", "1003"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(rendered, `GotoIf($["${DIALSTATUS}" = "ANSWER"]?done)`) != 2 {
		t.Fatalf("wrong number of answer checks for three steps:\n%s", rendered)
	}
	// In the order given, not sorted: a hunt order is a decision.
	first := strings.Index(rendered, "PJSIP/1001")
	second := strings.Index(rendered, "PJSIP/1002")
	third := strings.Index(rendered, "PJSIP/1003")
	if !(first < second && second < third) {
		t.Fatalf("the hunt order was not preserved:\n%s", rendered)
	}
	if !strings.Contains(rendered, " same => n(done),Hangup()\n") {
		t.Fatalf("no done label:\n%s", rendered)
	}
}

// A group naming an account that does not exist rings nothing, and the only
// way to find out is a caller who never reaches anyone.
func TestInboundPlanRefusesAnUnknownExtension(t *testing.T) {
	plan := InboundPlan{Mode: InboundHunt, RingSeconds: 30, HuntSeconds: 15,
		Extensions: []string{"1001", "9999"}}
	if err := plan.Validate(extensions("1001")); err == nil {
		t.Fatal("a ring group naming an unconfigured extension was accepted")
	}
}

func TestInboundPlanValidatesTheRest(t *testing.T) {
	configured := extensions("1001", "1002")
	for name, plan := range map[string]InboundPlan{
		"unknown mode":   {Mode: "queue", RingSeconds: 30},
		"no ring time":   {Mode: InboundRingAll},
		"ring too long":  {Mode: InboundRingAll, RingSeconds: 4000},
		"hunt with none": {Mode: InboundHunt, RingSeconds: 30, HuntSeconds: 15},
		"hunt step zero": {Mode: InboundHunt, RingSeconds: 30, Extensions: []string{"1001"}},
		"duplicate":      {Mode: InboundRingAll, RingSeconds: 30, Extensions: []string{"1001", "1001"}},
		"empty entry":    {Mode: InboundRingAll, RingSeconds: 30, Extensions: []string{""}},
	} {
		if err := plan.Validate(configured); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	valid := InboundPlan{Mode: InboundHunt, RingSeconds: 30, HuntSeconds: 15, Extensions: []string{"1001"}}
	if err := valid.Validate(configured); err != nil {
		t.Errorf("a valid plan was refused: %v", err)
	}
}

// extensions.conf includes this file unconditionally, so every mode must
// produce a parseable context even with nothing configured.
func TestRenderInboundAlwaysDefinesTheContext(t *testing.T) {
	rendered, err := RenderInbound(InboundPlan{Mode: InboundPerNumber, RingSeconds: 30}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered, "["+InboundContext+"]") {
		t.Fatalf("the context is missing:\n%s", rendered)
	}
	// Ring-all with nothing configured still renders: deleting every
	// extension is a thing someone may do, and refusing the save would make
	// it look like a mistake. The call is rejected with a truthful cause
	// rather than answered with silence.
	empty, err := RenderInbound(InboundPlan{Mode: InboundRingAll, RingSeconds: 30}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(empty, "Hangup(1)") {
		t.Fatalf("ring-all with nothing configured does not reject the call:\n%s", empty)
	}
	if strings.Contains(empty, "Dial(") {
		t.Fatalf("ring-all with nothing configured still dials something:\n%s", empty)
	}
}

// The default has to be the mode least likely to lose a call, because it is
// what runs before anyone configures anything.
func TestDefaultInboundPlanRingsEverything(t *testing.T) {
	plan := DefaultInboundPlan()
	if plan.Mode != InboundRingAll {
		t.Fatalf("default mode = %q", plan.Mode)
	}
	if err := plan.Validate(extensions("1001")); err != nil {
		t.Fatalf("the default plan does not validate: %v", err)
	}
}
