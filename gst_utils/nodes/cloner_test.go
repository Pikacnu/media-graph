package nodes

import (
	"errors"
	"slices"
	"strconv"
	"testing"

	"live_tools/gst_utils/core"

	"github.com/go-gst/go-gst/pkg/gst"
)

func newTestCloneNode(t *testing.T, id string) *CloneNode {
	t.Helper()
	gst.Init()
	node, err := CreateCloneNode(nil, CloneConfig{ID: id})
	if err != nil {
		t.Fatalf("CreateCloneNode: %v", err)
	}
	return node
}

// TestCloneNodeRemoveSrcPadKeepsOtherIDs pins the reason each branch carries its
// own id. A position-based id (index into a slice) shifts every later branch
// down by one on removal, so an id handed out earlier would silently start
// naming a different branch.
func TestCloneNodeRemoveSrcPadKeepsOtherIDs(t *testing.T) {
	node := newTestCloneNode(t, "cl")
	defer node.Destroy()

	var ids []string
	for i := 0; i < 3; i++ {
		_, id, err := node.RequestSrcPad(&core.PadInfo{ID: "cl_src_" + strconv.Itoa(i)})
		if err != nil {
			t.Fatalf("RequestSrcPad %d: %v", i, err)
		}
		ids = append(ids, id)
	}
	if want := []string{"cl_src_0", "cl_src_1", "cl_src_2"}; !slices.Equal(ids, want) {
		t.Fatalf("requested ids = %v, want %v", ids, want)
	}

	if err := node.RemoveSrcPad("cl_src_1"); err != nil {
		t.Fatalf("RemoveSrcPad: %v", err)
	}

	if got, want := node.GetSrcIDs(), []string{"cl_src_0", "cl_src_2"}; !slices.Equal(got, want) {
		t.Fatalf("GetSrcIDs after removal = %v, want %v", got, want)
	}

	// GetSrcPad reports the newest branch, which is now _src_2, not _src_1.
	_, latest, err := node.GetSrcPad()
	if err != nil {
		t.Fatalf("GetSrcPad: %v", err)
	}
	if latest != "cl_src_2" {
		t.Fatalf("GetSrcPad = %q, want %q", latest, "cl_src_2")
	}
}

// TestCloneNodeRemoveSrcPadDoesNotRecycleNumber pins the minting rule on the
// nil path: a freed number is never handed out again, so an id that was already
// reported to the frontend cannot come to mean a different branch.
func TestCloneNodeRemoveSrcPadDoesNotRecycleNumber(t *testing.T) {
	node := newTestCloneNode(t, "cl")
	defer node.Destroy()

	var ids []string
	for i := 0; i < 3; i++ {
		_, id, err := node.RequestSrcPad(nil)
		if err != nil {
			t.Fatalf("RequestSrcPad %d: %v", i, err)
		}
		ids = append(ids, id)
	}
	if want := []string{"cl_src_0", "cl_src_1", "cl_src_2"}; !slices.Equal(ids, want) {
		t.Fatalf("minted ids = %v, want %v", ids, want)
	}

	if err := node.RemoveSrcPad("cl_src_1"); err != nil {
		t.Fatalf("RemoveSrcPad: %v", err)
	}

	// The freed number 1 must not come back; the counter only ever grows.
	_, newID, err := node.RequestSrcPad(nil)
	if err != nil {
		t.Fatalf("RequestSrcPad after removal: %v", err)
	}
	if newID != "cl_src_3" {
		t.Fatalf("minted id after removal = %q, want %q (numbers must not be recycled)", newID, "cl_src_3")
	}
}

func TestCloneNodeRemoveSrcPadUnknownID(t *testing.T) {
	node := newTestCloneNode(t, "cl")
	defer node.Destroy()

	if _, _, err := node.RequestSrcPad(&core.PadInfo{ID: "cl_src_0"}); err != nil {
		t.Fatalf("RequestSrcPad: %v", err)
	}
	if err := node.RemoveSrcPad("cl_src_9"); !errors.Is(err, core.ErrGSTPadNotFound) {
		t.Fatalf("RemoveSrcPad(unknown) = %v, want %v", err, core.ErrGSTPadNotFound)
	}
}

// TestCloneNodeRemoveThenDestroy removes a branch and then tears the node down,
// so the released tee request pad and queue are not touched a second time.
func TestCloneNodeRemoveThenDestroy(t *testing.T) {
	node := newTestCloneNode(t, "cl")

	for i := 0; i < 3; i++ {
		if _, _, err := node.RequestSrcPad(&core.PadInfo{ID: "cl_src_" + strconv.Itoa(i)}); err != nil {
			t.Fatalf("RequestSrcPad %d: %v", i, err)
		}
	}
	if err := node.RemoveSrcPad("cl_src_0"); err != nil {
		t.Fatalf("RemoveSrcPad: %v", err)
	}
	if err := node.Destroy(); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if err := node.Destroy(); !errors.Is(err, core.ErrGSTNodeDestroyed) {
		t.Fatalf("second Destroy = %v, want %v", err, core.ErrGSTNodeDestroyed)
	}
}
