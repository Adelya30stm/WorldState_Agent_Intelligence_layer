package attackplan

import "fmt"

func applyPatch(currentNodes []Node, currentEdges []Edge, patch GraphPatch) ([]Node, []Edge, error) {
	removeNodes := make(map[string]struct{}, len(patch.RemoveNodeKeys))
	for _, key := range patch.RemoveNodeKeys {
		if key == "" {
			return nil, nil, ErrInvalidInput
		}
		removeNodes[key] = struct{}{}
	}
	upserts := make(map[string]Node, len(patch.UpsertNodes))
	for _, node := range patch.UpsertNodes {
		if _, removing := removeNodes[node.Key]; removing {
			return nil, nil, fmt.Errorf("%w: node %q is both removed and upserted", ErrInvalidInput, node.Key)
		}
		if _, duplicate := upserts[node.Key]; duplicate {
			return nil, nil, fmt.Errorf("%w: duplicate node patch %q", ErrInvalidInput, node.Key)
		}
		upserts[node.Key] = node
	}

	nodes := make([]Node, 0, len(currentNodes)+len(upserts))
	for _, node := range currentNodes {
		if _, removing := removeNodes[node.Key]; removing {
			continue
		}
		if replacement, ok := upserts[node.Key]; ok {
			nodes = append(nodes, replacement)
			delete(upserts, node.Key)
		} else {
			nodes = append(nodes, node)
		}
	}
	for _, node := range patch.UpsertNodes {
		if _, ok := upserts[node.Key]; ok {
			nodes = append(nodes, node)
			delete(upserts, node.Key)
		}
	}

	removeEdges := make(map[string]struct{}, len(patch.RemoveEdges))
	for _, edge := range patch.RemoveEdges {
		removeEdges[edgeIdentity(edge)] = struct{}{}
	}
	edges := make([]Edge, 0, len(currentEdges)+len(patch.AddEdges))
	for _, edge := range currentEdges {
		_, removeFrom := removeNodes[edge.FromKey]
		_, removeTo := removeNodes[edge.ToKey]
		_, removeEdge := removeEdges[edgeIdentity(edge)]
		if !removeFrom && !removeTo && !removeEdge {
			edges = append(edges, edge)
		}
	}
	edges = append(edges, patch.AddEdges...)
	if err := validateGraph(nodes, edges); err != nil {
		return nil, nil, err
	}
	return nodes, edges, nil
}

func edgeIdentity(edge Edge) string {
	return edge.FromKey + "\x00" + edge.ToKey + "\x00" + string(edge.Kind)
}
