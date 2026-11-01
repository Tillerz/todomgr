package main

import (
	"fmt"
	"time"
)

// schemaVersion is the structure version of database.json. Every time the data
// structure changes, bump it and add a migration for the previous version.
// A file is migrated step by step (N -> N+1 -> ...) when the app loads it.
// A file with a HIGHER schema than the app knows is never touched: the app
// refuses to open it instead of risking data loss.
//
// History:
//
//	0  prototype exports (browser localStorage JSON, key "v":2, activity inside)
//	1  first Go version: schema/app/rev fields, activity moved to activity.jsonl
var schemaVersion = 1

type migration func(raw map[string]any) error

// migrations[n] upgrades a document from schema n to schema n+1.
var migrations = map[int]migration{
	0: migrate0to1,
}

func migrate0to1(raw map[string]any) error {
	nodes, ok := raw["nodes"].(map[string]any)
	if !ok {
		return fmt.Errorf("nodes is not an object")
	}
	if _, ok := nodes["root"]; !ok {
		nodes["root"] = map[string]any{"id": "root", "name": "root", "children": []any{}}
	}
	for id, v := range nodes {
		m, ok := v.(map[string]any)
		if !ok {
			return fmt.Errorf("node %s is malformed", id)
		}
		if id == "root" {
			if _, ok := m["children"]; !ok {
				m["children"] = []any{}
			}
			continue
		}
		fillDefaults(Node(m), id)
	}
	if _, ok := raw["prefs"].(map[string]any); !ok {
		raw["prefs"] = map[string]any{}
	}
	return nil
}

// fillDefaults adds every field a node should have but is missing.
func fillDefaults(n Node, id string) {
	d := defaultNode(id, str(n["name"]))
	for k, v := range d {
		if _, ok := n[k]; !ok {
			n[k] = v
		}
	}
	n["id"] = id
	if _, ok := n["created"]; !ok {
		n["created"] = float64(time.Now().UnixMilli())
	}
}
