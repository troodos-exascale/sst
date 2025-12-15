package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/swiss-software-tool/runtime-smart/go/internal/sst"
	"gopkg.in/yaml.v3"
)

//
// =========================
// Domain model (YAML types)
// =========================

type genericComponent struct {
	Kind string `yaml:"kind"`
}

type componentDef struct {
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec struct {
		Provides interface{} `yaml:"provides"`
		Requires interface{} `yaml:"requires"`
	} `yaml:"spec"`
}

type OSImagePlanSpec struct {
	StorageLayout string   `yaml:"storageLayout"`
	Components    []string `yaml:"components"`
}

type OSImagePlan struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec OSImagePlanSpec `yaml:"spec"`
}

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

	definitions map[string]*componentDef
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
			definitions:    make(map[string]*componentDef),
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

		data, err := os.ReadFile(foundPath)
		if err != nil {
			return err
		}

		var gen genericComponent
		if err := yaml.Unmarshal(data, &gen); err != nil {
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

			var def componentDef
			if err := yaml.Unmarshal(data, &def); err != nil {
				return err
			}
			def.Metadata.Name = name
			app.definitions[name] = &def

			if provides, ok := def.Spec.Provides.([]interface{}); ok {
				for _, p := range provides {
					if pm, ok := p.(map[string]interface{}); ok {
						for k := range pm {
							app.providesMap[k] = name
						}
					}
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
		if requires, ok := def.Spec.Requires.([]interface{}); ok {
			for _, r := range requires {
				switch v := r.(type) {
				case string:
					if _, ok := app.definitions[v]; !ok {
						return fmt.Errorf(
							"component '%s' requires '%s', which is not selected",
							def.Metadata.Name, v,
						)
					}
				case map[string]interface{}:
					for k := range v {
						if _, ok := app.providesMap[k]; !ok {
							return fmt.Errorf(
								"component '%s' requires capability '%s', which is not provided",
								def.Metadata.Name, k,
							)
						}
					}
				}
			}
		}
	}
	return nil
}

func composeWritePlan(app *composeApp) error {
	plan := OSImagePlan{
		APIVersion: "sst.io/v1",
		Kind:       "OSImagePlan",
		Metadata:   struct{ Name string `yaml:"name"` }{Name: app.outputName},
		Spec: OSImagePlanSpec{
			StorageLayout: app.storageLayout,
			Components:    app.osComponents,
		},
	}

	data, err := yaml.Marshal(&plan)
	if err != nil {
		return err
	}

	out := app.outputName + ".plan.yaml"
	if err := os.WriteFile(out, data, 0644); err != nil {
		return err
	}

	fmt.Printf("Successfully generated validated plan: %s\n", out)
	return nil
}
