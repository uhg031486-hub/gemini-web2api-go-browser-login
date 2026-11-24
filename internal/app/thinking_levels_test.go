package app

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestCurrentModelThinkingLevels(t *testing.T) {
	withPoolCookie(t)
	for _, family := range []struct {
		name, hex string
		mode      int
	}{
		{"gemini-4-hydrogen", "56fdd199312815e2", 1},
		{"gemini-4-neon", "e6fa609c3fa255c0", 3},
	} {
		for _, level := range []struct {
			suffix     string
			wire, flag int
		}{
			{"", 1, 0}, {"-low", 1, 0}, {"-medium", 4, 1}, {"-high", 2, 1}, {"-thinking", 2, 1},
		} {
			t.Run(family.name+level.suffix, func(t *testing.T) {
				_, mc, err := resolveModel(family.name + level.suffix)
				if err != nil {
					t.Fatal(err)
				}
				if mc.HexID != family.hex || mc.Mode != family.mode {
					t.Fatalf("wrong route: %+v", mc)
				}
				if !modelNeedsLogin(mc) {
					t.Fatal("signed-in route exposed anonymously")
				}
				inner := make([]interface{}, innerSlots)
				setThinkingFields(inner, mc)
				if inner[80] != level.wire || inner[96] != level.flag {
					t.Fatalf("payload tier=%v flag=%v", inner[80], inner[96])
				}
				var header []interface{}
				if err := json.Unmarshal([]byte(buildModelHeader(mc.HexID, mc.Mode, mc.thinkWire(), "fixture-uuid")), &header); err != nil {
					t.Fatal(err)
				}
				if header[14] != float64(family.mode) || header[15] != float64(level.wire) {
					t.Fatalf("header mode/tier mismatch: %v", header)
				}
			})
		}
	}
}

func TestLegacyThinkingEncodingUnchanged(t *testing.T) {
	for _, name := range []string{"gemini-3.6-flash", "gemini-3.5-flash-lite", "gemini-3.1-pro", "gemini-3.8-flash", "gemini-4-argon"} {
		if got := Models[name].thinkWire(); got != 1 {
			t.Fatalf("%s changed normal tier: %d", name, got)
		}
		if got := Models[name+"-thinking"].thinkWire(); got != 2 {
			t.Fatalf("%s changed extended tier: %d", name, got)
		}
	}
	// An explicit medium tier must override the old boolean.
	if got := (ModelConfig{Thinking: true, ThinkWire: 4}).thinkWire(); got != 4 {
		t.Fatalf("explicit tier ignored: %d", got)
	}
}

func TestAutoHasNoSelectableThinkingTiers(t *testing.T) {
	withPoolCookie(t)
	_, mc, err := resolveModel("gemini-auto")
	if err != nil {
		t.Fatal(err)
	}
	if mc.HexID != hexFlashLite || mc.Mode != 6 || mc.Thinking {
		t.Fatalf("wrong Auto route: %+v", mc)
	}
	for _, suffix := range []string{"-low", "-medium", "-high", "-thinking"} {
		if _, _, err := resolveModel("gemini-auto" + suffix); err == nil {
			t.Fatalf("Auto must not expose %s", suffix)
		}
	}
}

func TestModelEndpointListsCurrentNames(t *testing.T) {
	withPoolCookie(t)
	w := httptest.NewRecorder()
	handleModels(w, httptest.NewRequest("GET", "/v1/models", nil))
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, m := range body.Data {
		ids[m.ID] = true
	}
	for _, name := range []string{"gemini-auto", "gemini-4-hydrogen-low", "gemini-4-hydrogen-medium", "gemini-4-hydrogen-high", "gemini-4-neon-low", "gemini-4-neon-medium", "gemini-4-neon-high"} {
		if !ids[name] {
			t.Errorf("catalog missing %s", name)
		}
	}
	for _, legacy := range []string{"gemini-3.8-flash", "gemini-3.1-pro"} {
		if ids[legacy] {
			t.Errorf("legacy name must be accepted but hidden from new catalog: %s", legacy)
		}
		if _, _, err := resolveModel(legacy); err != nil {
			t.Errorf("legacy config broken: %v", err)
		}
	}
}
