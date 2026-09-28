package architecture

import (
	"encoding/json"
	"io"
)

// Graph is a stable package dependency snapshot.
type Graph struct {
	Packages []Package `json:"packages"`
	Edges    []Edge    `json:"edges"`
}

// Package describes one Go package in a graph snapshot.
type Package struct {
	Path   string `json:"path"`
	LOC    int    `json:"loc"`
	FanIn  int    `json:"fan_in"`
	FanOut int    `json:"fan_out"`
}

// Edge is a directed package import.
type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// LoadGraph decodes a graph fixture.
func LoadGraph(r io.Reader) (Graph, error) {
	var graph Graph
	err := json.NewDecoder(r).Decode(&graph)
	return graph, err
}
