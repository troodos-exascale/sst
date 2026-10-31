// Copyright 2025-2026 Peter Braam
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	sst "github.com/troodos-exascale/sst/internal/sst"

	composepb "github.com/troodos-exascale/sst/proto/yaml/compose/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

//
// =========================
// Application root
// =========================

type composeApp struct {
	// inputs
	outputName     string
	withComponents []string

	// derived state
	repoRoot      string
	searchPaths   []string
	storageLayout string
	osComponents  []string

	// component defs keyed by name
	definitions map[string]*composepb.ComponentDef

	// capability -> provider name
	providesMap map[string]string

	// behavior + framework
	methods composeMethods
	steps   *sst.StepsRunState
}

//
// =========================
// Methods table
// =========================

type composeMethods struct {
	ResolveRepo    func(*composeApp) error
	LoadComponents func(*composeApp) error
	ValidateDeps   func(*composeApp) error
	WritePlan      func(*composeApp) error
}

func newComposeMethods() composeMethods {
	m := composeMethods{
		ResolveRepo:    composeResolveRepo,
		LoadComponents: composeLoadComponents,
		ValidateDeps:   composeValidateDeps,
		WritePlan:      composeWritePlan,
	}
	m.mustValidate()
	return m
}

func (m composeMethods) mustValidate() {
	if m.ResolveRepo == nil ||
		m.LoadComponents == nil ||
		m.ValidateDeps == nil ||
		m.WritePlan == nil {
		panic("composeMethods: missing required method")
	}
}

//
// =========================
// Cobra command
// =========================

var composeCmd = &cobra.Command{
	Use:   "compose <output-name> --with <component> [--with ...]",
	Short: "Composes components into a validated plan file.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {

		withComponents, _ := cmd.Flags().GetStringSlice("with")

		app := &composeApp{
			outputName:     args[0],
			withComponents: withComponents,
			definitions:    make(map[string]*composepb.ComponentDef),
			providesMap:    make(map[string]string),
			methods:        newComposeMethods(),
		}

		app.steps = sst.StepsInit("compose")

		app.steps.StepsAdd("resolve repo", func(ctx *sst.StepsContext, _ sst.StepState) error {
			return app.methods.ResolveRepo(app)
		})
		app.steps.StepsAdd("load components", func(ctx *sst.StepsContext, _ sst.StepState) error {
			return app.methods.LoadComponents(app)
		})
		app.steps.StepsAdd("validate dependencies", func(ctx *sst.StepsContext, _ sst.StepState) error {
			return app.methods.ValidateDeps(app)
		})
		app.steps.StepsAdd("write plan", func(ctx *sst.StepsContext, _ sst.StepState) error {
			return app.methods.WritePlan(app)
		})

		return sst.StepsRun(app.steps)
	},
}

func init() {
	rootCmd.AddCommand(composeCmd)
	composeCmd.Flags().StringSlice(
		"with",
		[]string{},
		"Specify components (storage layout, OS, packages) to include.",
	)
}

//
// =========================
// YAML/proto loading helpers
// =========================

// loadGeneric loads only "kind" from a component manifest.
func loadGeneric(path string) (*composepb.GenericComponent, error) {
	ctx := context.Background()
	log := sst.LogxRoot().Subsystem("compose.load")

	gen := &composepb.GenericComponent{}
	yo := &sst.YamlObject{
		Path:   path,
		Schema: "sst.yaml.compose.v1.GenericComponent",
		Kind:   "GenericComponent",
		Msg:    gen,
	}
	if diags := yo.Load(ctx, log); diags != nil {
		return nil, diags
	}
	return gen, nil
}

// loadComponentDef loads the full component definition (metadata/spec).
func loadComponentDef(path string) (*composepb.ComponentDef, error) {
	ctx := context.Background()
	log := sst.LogxRoot().Subsystem("compose.load")

	def := &composepb.ComponentDef{}
	yo := &sst.YamlObject{
		Path:   path,
		Schema: "sst.yaml.compose.v1.ComponentDef",
		Kind:   "ComponentDef",
		Msg:    def,
	}
	if diags := yo.Load(ctx, log); diags != nil {
		return nil, diags
	}
	return def, nil
}

// extractCapabilities converts "provides" YAML (list of maps) into a flat list of keys.
// This preserves your current semantics: it only cares about the map keys.
func extractCapabilities(v *structpb.Value) []string {
	if v == nil {
		return nil
	}
	lv := v.GetListValue()
	if lv == nil {
		return nil
	}
	var out []string
	for _, item := range lv.Values {
		sv := item.GetStructValue()
		if sv == nil {
			continue
		}
		for k := range sv.Fields {
			out = append(out, k)
		}
	}
	return out
}

// extractRequires splits "requires" into:
// - direct component requires (strings)
// - capability requires (map keys)
func extractRequires(v *structpb.Value) (reqComponents []string, reqCaps []string) {
	if v == nil {
		return nil, nil
	}
	lv := v.GetListValue()
	if lv == nil {
		return nil, nil
	}
	for _, item := range lv.Values {
		switch item.Kind.(type) {
		case *structpb.Value_StringValue:
			reqComponents = append(reqComponents, item.GetStringValue())
		case *structpb.Value_StructValue:
			sv := item.GetStructValue()
			for k := range sv.Fields {
				reqCaps = append(reqCaps, k)
			}
		}
	}
	return reqComponents, reqCaps
}

//
// =========================
// Method implementations
// =========================

func composeResolveRepo(app *composeApp) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	app.repoRoot = root

	app.searchPaths = []string{
		filepath.Join(root, "templates/s2/sdk/storage-layouts"),
		filepath.Join(root, "templates/s2/sdk/os-components"),
		filepath.Join(root, "templates/s2/sdk/languages"),
	}
	return nil
}

func composeLoadComponents(app *composeApp) error {
	for _, name := range app.withComponents {
		var foundPath string
		for _, dir := range app.searchPaths {
			p := filepath.Join(dir, name+".yaml")
			if _, err := os.Stat(p); err == nil {
				foundPath = p
				break
			}
		}
		if foundPath == "" {
			return fmt.Errorf("could not find component manifest for '%s'", name)
		}

		gen, err := loadGeneric(foundPath)
		if err != nil {
			return err
		}

		switch gen.Kind {
		case "StorageLayout":
			if app.storageLayout != "" {
				return fmt.Errorf(
					"multiple storage layouts specified ('%s' and '%s')",
					app.storageLayout, name,
				)
			}
			app.storageLayout = name

		case "OSComponent":
			app.osComponents = append(app.osComponents, name)

			def, err := loadComponentDef(foundPath)
			if err != nil {
				return err
			}
			// Preserve old behavior: the name is the filename base.
			if def.Metadata == nil {
				def.Metadata = &composepb.ComponentMetadata{}
			}
			def.Metadata.Name = name
			app.definitions[name] = def

			// Build providesMap exactly as before: capability key -> component name.
			if def.Spec != nil {
				for _, cap := range extractCapabilities(def.Spec.Provides) {
					app.providesMap[cap] = name
				}
			}

		default:
			return fmt.Errorf("unknown component kind '%s' in %s", gen.Kind, foundPath)
		}
	}

	if app.storageLayout == "" {
		return fmt.Errorf("no storage layout component was specified")
	}
	return nil
}

func composeValidateDeps(app *composeApp) error {
	for _, name := range app.osComponents {
		def := app.definitions[name]
		if def == nil || def.Spec == nil {
			continue
		}

		reqComps, reqCaps := extractRequires(def.Spec.Requires)

		// direct component requires
		for _, rc := range reqComps {
			if _, ok := app.definitions[rc]; !ok {
				return fmt.Errorf(
					"component '%s' requires '%s', which is not selected",
					def.Metadata.GetName(), rc,
				)
			}
		}

		// capability requires
		for _, cap := range reqCaps {
			if _, ok := app.providesMap[cap]; !ok {
				return fmt.Errorf(
					"component '%s' requires capability '%s', which is not provided",
					def.Metadata.GetName(), cap,
				)
			}
		}
	}
	return nil
}

func composeWritePlan(app *composeApp) error {
	ctx := context.Background()
	log := sst.LogxRoot().Subsystem("compose.write")

	plan := &composepb.OSImagePlan{
		ApiVersion: "sst.io/v1",
		Kind:       "OSImagePlan",
		Metadata: &composepb.PlanMetadata{
			Name: app.outputName,
		},
		Spec: &composepb.OSImagePlanSpec{
			StorageLayout: app.storageLayout,
			Components:    append([]string{}, app.osComponents...),
		},
	}

	out := app.outputName + ".plan.yaml"

	yo := &sst.YamlObject{
		Path:   out,
		Schema: "sst.yaml.compose.v1.OSImagePlan",
		Kind:   "OSImagePlan",
		Msg:    plan,
	}

	if diags := yo.Save(ctx, log); diags != nil {
		return diags
	}

	fmt.Printf("Successfully generated validated plan: %s\n", out)
	return nil
}

//
// =========================
// Small helpers
// =========================

func sanitizePath(s string) string {
	s = strings.TrimSpace(s)
	return s
}
