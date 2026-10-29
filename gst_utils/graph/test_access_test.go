package graph

import "live_tools/gst_utils/core"

// nodesForTest exposes the live node map to white-box tests in this package.
//
// MediaGraph deliberately has no exported.nodesForTest() accessor: it would hand out
// live core.Node values (not copies), so a caller could mutate the graph through
// it while believing the map copy made it read-only. Production code reaches
// nodes through Snapshot (state) or the arity interfaces (capabilities); only
// tests need the raw instances, and only tests are allowed to know that.
//
// The read lock is taken for symmetry with the rest of the type. Tests run
// sequentially, so this never contends, but it keeps the field's invariant
// ("every read goes through mg.mu") true in test code as well.
func (mg *mediaGraphImpl) nodesForTest() map[string]core.Node {
	mg.mu.RLock()
	defer mg.mu.RUnlock()

	nodes := make(map[string]core.Node, len(mg.currentGraphNodes))
	for id, node := range mg.currentGraphNodes {
		nodes[id] = node
	}
	return nodes
}
