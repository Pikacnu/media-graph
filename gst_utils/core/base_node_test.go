package core

import "testing"

// newTestBase builds a BaseNode with no bin. BaseNode never dereferences the bin
// in the property methods under test, so nil is safe here.
func newTestBase(id string) *BaseNode {
	return NewBaseNode(id, "TestNode", nil)
}

func TestUpdatePropertiesMergesAndDeletes(t *testing.T) {
	base := newTestBase("n1")

	if err := base.UpdateProperties(map[string]interface{}{"A": 1, "B": "x"}); err != nil {
		t.Fatalf("UpdateProperties(set): %v", err)
	}
	props := base.Properties()
	if props["A"] != 1 || props["B"] != "x" {
		t.Fatalf("Properties() = %v, want A=1 B=x", props)
	}

	// A nil value is a delete, not an error.
	if err := base.UpdateProperties(map[string]interface{}{"A": nil}); err != nil {
		t.Fatalf("UpdateProperties(delete): %v", err)
	}
	props = base.Properties()
	if _, exists := props["A"]; exists {
		t.Errorf("Properties() still has A after delete: %v", props)
	}
	if props["B"] != "x" {
		t.Errorf("Properties() lost B: %v", props)
	}
}

// A protected key is DROPPED, not rejected: the graph sends a node's whole
// property map on update, and constructor-fixed config is expected to be in it.
func TestUpdatePropertiesDropsProtectedKey(t *testing.T) {
	base := newTestBase("n1")
	if err := base.UpdateProperties(map[string]interface{}{"Caps": "video/x-raw", "Keep": 1}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	base.ProtectProperties("Caps")

	// The whole map arrives, protected key included. This must succeed.
	if err := base.UpdateProperties(map[string]interface{}{"Caps": "video/x-h264", "Keep": 2}); err != nil {
		t.Fatalf("UpdateProperties with protected key: %v", err)
	}

	props := base.Properties()
	if props["Caps"] != "video/x-raw" {
		t.Errorf("Caps = %v, want the protected value video/x-raw", props["Caps"])
	}
	if props["Keep"] != 2 {
		t.Errorf("Keep = %v, want the unprotected update to land (2)", props["Keep"])
	}
}

// ID is the one key that can still fail: a node cannot become another node, and
// deleting its identity would orphan its pads.
func TestUpdatePropertiesIDRules(t *testing.T) {
	base := newTestBase("n1")

	if err := base.UpdateProperties(map[string]interface{}{"ID": "n1"}); err != nil {
		t.Errorf("ID equal to own ID = %v, want nil", err)
	}
	if err := base.UpdateProperties(map[string]interface{}{"ID": "other"}); err == nil {
		t.Error("ID change to a different value = nil, want error")
	}
	if err := base.UpdateProperties(map[string]interface{}{"ID": nil}); err == nil {
		t.Error("ID deletion = nil, want error")
	}
	if base.ID() != "n1" {
		t.Errorf("ID() = %q, want n1", base.ID())
	}
}

// Properties() must hand back a copy; otherwise a caller could mutate the node
// through the returned map.
func TestPropertiesReturnsCopy(t *testing.T) {
	base := newTestBase("n1")
	if err := base.UpdateProperties(map[string]interface{}{"A": 1}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	props := base.Properties()
	props["A"] = 999
	delete(props, "A")

	if got := base.Properties()["A"]; got != 1 {
		t.Errorf("A = %v after mutating the returned map, want 1", got)
	}
}

// Clear drops the bin and the property store, but protection is a property of
// the type, so it survives.
func TestClearKeepsProtection(t *testing.T) {
	base := newTestBase("n1")
	if err := base.UpdateProperties(map[string]interface{}{"Caps": "v"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	base.ProtectProperties("Caps")

	base.Clear()

	if !base.Destroyed() {
		t.Error("Destroyed() = false after Clear")
	}
	if len(base.Properties()) != 0 {
		t.Errorf("Properties() = %v after Clear, want empty", base.Properties())
	}
	if err := base.UpdateProperties(map[string]interface{}{"Caps": "other"}); err != nil {
		t.Fatalf("UpdateProperties after Clear: %v", err)
	}
	if _, exists := base.Properties()["Caps"]; exists {
		t.Error("Caps was stored after Clear, want it still protected")
	}
}
