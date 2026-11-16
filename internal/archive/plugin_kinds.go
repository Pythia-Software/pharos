package archive

import (
	"sort"
	"strings"
)

// Drop forms an importer plugin may use (docs/plugin-imports.md).
const (
	importFiles         = "files"          // a mirror of the source's folder, captured with the kind's Plan
	importCaptureExport = "capture-export" // a verified capture-export envelope
	importExportFiles   = "export"         // provider export files, handed to the export collector
)

// providerImport declares how a kind may arrive through an importer plugin.
// A kind without one is refused by name. The declaration is published as
// contracts/plugins/kinds.json, which a test holds equal to the registry.
type providerImport struct {
	Forms []string
	// Roots are the kind's usual folders on any operating system, as slash
	// paths below the home. They tell importers where to look when asked;
	// Pharos itself reads none of them.
	Roots []string
	// Files are the globs, below a root, of what the kind's Plan captures as
	// files; Databases those of what it captures as SQLite snapshots. "*"
	// stays inside one path component and "**/" spans any number of them.
	Files, Databases []string
}

// pluginKind is one entry of contracts/plugins/kinds.json.
type pluginKind struct {
	Kind          string                        `json:"kind"`
	Aliases       []string                      `json:"aliases,omitempty"`
	Name          string                        `json:"name"`
	Forms         []string                      `json:"forms"`
	Roots         []string                      `json:"roots,omitempty"`
	Files         []string                      `json:"files,omitempty"`
	Databases     []string                      `json:"databases,omitempty"`
	CaptureTables map[string]pluginCaptureTable `json:"capture_tables,omitempty"`
	Content       pluginKindContent             `json:"content"`
}

type pluginCaptureTable struct {
	Columns string `json:"columns,omitempty"`
	Where   string `json:"where,omitempty"`
}

type pluginKindContent struct {
	Supported     []string          `json:"supported,omitempty"`
	Varies        []string          `json:"varies,omitempty"`
	NotApplicable []string          `json:"not_applicable,omitempty"`
	Notes         map[string]string `json:"notes,omitempty"`
}

type pluginKindsDocument struct {
	Contract string       `json:"contract"`
	Kinds    []pluginKind `json:"kinds"`
}

// pluginKinds lists, in registry order, the kinds an importer may drop.
func pluginKinds() pluginKindsDocument {
	document := pluginKindsDocument{Contract: "pharos-plugin-kinds/1"}
	for _, spec := range providerRegistry {
		if spec.Import == nil {
			continue
		}
		kind := pluginKind{
			Kind: spec.Kind, Aliases: spec.KindAliases, Name: spec.DisplayName,
			Forms: spec.Import.Forms, Roots: spec.Import.Roots, Files: spec.Import.Files, Databases: spec.Import.Databases,
			Content: pluginKindContent{
				Supported: strings.Fields(spec.Content.Supported), Varies: strings.Fields(spec.Content.Varies),
				NotApplicable: strings.Fields(spec.Content.NotApplicable), Notes: spec.Content.Notes,
			},
		}
		for _, list := range [][]string{kind.Content.Supported, kind.Content.Varies, kind.Content.NotApplicable} {
			sort.Strings(list)
		}
		for table, keep := range spec.CaptureTables {
			if kind.CaptureTables == nil {
				kind.CaptureTables = map[string]pluginCaptureTable{}
			}
			kind.CaptureTables[table] = pluginCaptureTable{Columns: keep.columns, Where: keep.where}
		}
		document.Kinds = append(document.Kinds, kind)
	}
	return document
}
