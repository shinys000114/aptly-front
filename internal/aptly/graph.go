package aptly

import (
	"context"
	"fmt"
	"strings"
)

type Graph struct {
	Nodes []GraphNode `json:"nodes"`
	Edges []GraphEdge `json:"edges"`
}

type GraphNode struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Type  string `json:"type"`
}

type GraphEdge struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Label string `json:"label,omitempty"`
}

func (c *Client) BuildGraph(ctx context.Context) (Graph, error) {
	repos, repoErr := c.ListRepos(ctx)
	mirrors, mirrorErr := c.ListMirrors(ctx)
	snapshots, snapshotErr := c.ListSnapshots(ctx)
	publishes, publishErr := c.ListPublishes(ctx)

	if repoErr != nil {
		return Graph{}, repoErr
	}
	if mirrorErr != nil {
		return Graph{}, mirrorErr
	}
	if snapshotErr != nil {
		return Graph{}, snapshotErr
	}
	if publishErr != nil {
		return Graph{}, publishErr
	}

	builder := graphBuilder{
		seenNodes: map[string]bool{},
		seenEdges: map[string]bool{},
	}
	repoNames := map[string]bool{}
	mirrorNames := map[string]bool{}
	snapshotNames := map[string]bool{}

	for _, repo := range repos {
		repoNames[repo.Name] = true
		builder.node("repo:"+repo.Name, repo.Name, "repo")
	}
	for _, mirror := range mirrors {
		mirrorNames[mirror.Name] = true
		builder.node("mirror:"+mirror.Name, mirror.Name, "mirror")
	}
	for _, snapshot := range snapshots {
		snapshotNames[snapshot.Name] = true
	}

	for _, snapshot := range snapshots {
		builder.node("snapshot:"+snapshot.Name, snapshot.Name, "snapshot")

		baseSourceKind := normalizeSourceKind(snapshot.SourceKind)
		for _, sourceID := range snapshot.SourceIDs {
			sourceKind := inferSourceKind(baseSourceKind, sourceID, repoNames, mirrorNames, snapshotNames)
			switch sourceKind {
			case "repo":
				builder.node("repo:"+sourceID, sourceID, "repo")
				builder.edge("repo:"+sourceID, "snapshot:"+snapshot.Name, "snapshot")
			case "mirror":
				builder.node("mirror:"+sourceID, sourceID, "mirror")
				builder.edge("mirror:"+sourceID, "snapshot:"+snapshot.Name, "snapshot")
			case "snapshot", "merge", "pull":
				builder.node("snapshot:"+sourceID, sourceID, "snapshot")
				builder.edge("snapshot:"+sourceID, "snapshot:"+snapshot.Name, sourceKind)
			default:
				if sourceID != "" {
					builder.sourceEdge(sourceKind, sourceID, "snapshot:"+snapshot.Name, sourceKind)
				}
			}
		}
	}

	for _, publish := range publishes {
		id := fmt.Sprintf("publish:%s:%s:%s", publish.Storage, publish.Prefix, publish.Distribution)
		label := publish.Distribution
		if publish.Prefix != "" && publish.Prefix != "." {
			label = publish.Prefix + "/" + publish.Distribution
		}
		if publish.Storage != "" {
			label = publish.Storage + ":" + label
		}
		builder.node(id, label, "publish")
		sourceKind := normalizeSourceKind(publish.SourceKind)
		if sourceKind == "" {
			sourceKind = "snapshot"
		}
		for _, source := range publish.Sources {
			if source.Name == "" {
				continue
			}
			inferredKind := inferSourceKind(sourceKind, source.Name, repoNames, mirrorNames, snapshotNames)
			switch inferredKind {
			case "repo":
				builder.node("repo:"+source.Name, source.Name, "repo")
				builder.edge("repo:"+source.Name, id, source.Component)
			case "snapshot":
				builder.node("snapshot:"+source.Name, source.Name, "snapshot")
				builder.edge("snapshot:"+source.Name, id, source.Component)
			case "mirror":
				builder.node("mirror:"+source.Name, source.Name, "mirror")
				builder.edge("mirror:"+source.Name, id, source.Component)
			default:
				builder.sourceEdge(inferredKind, source.Name, id, source.Component)
			}
		}
	}

	return Graph{Nodes: builder.nodes, Edges: builder.edges}, nil
}

type graphBuilder struct {
	nodes     []GraphNode
	edges     []GraphEdge
	seenNodes map[string]bool
	seenEdges map[string]bool
}

func (b *graphBuilder) node(id, label, nodeType string) {
	if id == "" || b.seenNodes[id] {
		return
	}
	if label == "" {
		label = id
	}
	b.seenNodes[id] = true
	b.nodes = append(b.nodes, GraphNode{ID: id, Label: label, Type: nodeType})
}

func (b *graphBuilder) edge(from, to, label string) {
	if from == "" || to == "" {
		return
	}
	key := from + "\x00" + to + "\x00" + label
	if b.seenEdges[key] {
		return
	}
	b.seenEdges[key] = true
	b.edges = append(b.edges, GraphEdge{From: from, To: to, Label: label})
}

func (b *graphBuilder) sourceEdge(kind, sourceID, to, label string) {
	if kind == "" {
		kind = "unknown"
	}
	id := kind + ":" + sourceID
	b.node(id, sourceID, kind)
	b.edge(id, to, label)
}

func normalizeSourceKind(kind string) string {
	kind = strings.ToLower(strings.TrimSpace(kind))
	kind = strings.ReplaceAll(kind, "_", " ")
	kind = strings.ReplaceAll(kind, "-", " ")

	switch {
	case strings.Contains(kind, "local") || strings.Contains(kind, "repo"):
		return "repo"
	case strings.Contains(kind, "mirror"):
		return "mirror"
	case strings.Contains(kind, "snapshot"):
		return "snapshot"
	case strings.Contains(kind, "merge"):
		return "merge"
	case strings.Contains(kind, "pull"):
		return "pull"
	default:
		return kind
	}
}

func inferSourceKind(kind, sourceID string, repos, mirrors, snapshots map[string]bool) string {
	if repos[sourceID] {
		return "repo"
	}
	if mirrors[sourceID] {
		return "mirror"
	}
	if snapshots[sourceID] {
		if kind == "merge" || kind == "pull" {
			return kind
		}
		return "snapshot"
	}
	return kind
}
