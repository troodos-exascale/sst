package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

type genericComponent struct {
	Kind string `yaml:"kind"`
}

type componentDef struct {
	// CORRECTED: Removed unused 'path' field.
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

var composeCmd = &cobra.Command{
	Use:   "compose <output-name> --with <component> [--with ...]",
	Short: "Composes components into a validated plan file.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		outputName := args[0]
		withComponents, _ := cmd.Flags().GetStringSlice("with")

		repoRoot := core.Files().Root()
		
		searchPaths := []string{
			filepath.Join(repoRoot, "templates/s2/sdk/storage-layouts"),
			filepath.Join(repoRoot, "templates/s2/sdk/os-components"),
			filepath.Join(repoRoot, "templates/s2/sdk/languages"),
		}

		var storageLayoutComponent string
		var osComponents []string
		definitions := make(map[string]*componentDef)
		providesMap := make(map[string]string)

		for _, name := range withComponents {
			var foundPath string
			for _, dir := range searchPaths {
				path := filepath.Join(dir, name+".yaml")
				if _, err := os.Stat(path); err == nil {
					foundPath = path
					break
				}
			}

			if foundPath == "" {
				return fmt.Errorf("could not find component manifest for '%s'", name)
			}

			data, err := os.ReadFile(foundPath)
			if err != nil { return err }

			var genComp genericComponent
			if err := yaml.Unmarshal(data, &genComp); err != nil { return err }

			switch genComp.Kind {
			case "StorageLayout":
				if storageLayoutComponent != "" {
					return fmt.Errorf("multiple storage layouts specified ('%s' and '%s')", storageLayoutComponent, name)
				}
				storageLayoutComponent = name
			case "OSComponent":
				osComponents = append(osComponents, name)
				var def componentDef
				if err := yaml.Unmarshal(data, &def); err != nil { return err }
				def.Metadata.Name = name
				definitions[def.Metadata.Name] = &def
				if provides, ok := def.Spec.Provides.([]interface{}); ok {
					for _, p := range provides {
						if pMap, ok := p.(map[string]interface{}); ok {
							for key := range pMap {
								providesMap[key] = def.Metadata.Name
							}
						}
					}
				}
			default:
				return fmt.Errorf("unknown component kind '%s' in manifest %s", genComp.Kind, foundPath)
			}
		}

		if storageLayoutComponent == "" {
			return fmt.Errorf("no storage layout component was specified")
		}

		for _, name := range osComponents {
			def := definitions[name]
			if requires, ok := def.Spec.Requires.([]interface{}); ok {
				for _, r := range requires {
					if rStr, ok := r.(string); ok {
						if _, exists := definitions[rStr]; !exists {
							return fmt.Errorf("dependency error: component '%s' requires '%s', which is not selected", def.Metadata.Name, rStr)
						}
					} else if rMap, ok := r.(map[string]interface{}); ok {
						for key := range rMap {
							if _, exists := providesMap[key]; !exists {
								return fmt.Errorf("dependency error: component '%s' requires capability '%s', which is not provided", def.Metadata.Name, key)
							}
						}
					}
				}
			}
		}

		fmt.Println("✅ All component dependencies have been successfully resolved.")

		plan := OSImagePlan{
			APIVersion: "sst.io/v1",
			Kind:       "OSImagePlan",
			Metadata:   struct{ Name string `yaml:"name"` }{Name: outputName},
			Spec: OSImagePlanSpec{
				StorageLayout: storageLayoutComponent,
				Components:    osComponents,
			},
		}

		planBytes, err := yaml.Marshal(&plan)
		if err != nil { return err }
		outputFile := outputName + ".plan.yaml"
		if err := os.WriteFile(outputFile, planBytes, 0644); err != nil {
			return err
		}
		
		fmt.Printf("Successfully generated validated plan: %s\n", outputFile)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(composeCmd)
	composeCmd.Flags().StringSlice("with", []string{}, "Specify components (storage layout, OS, packages) to include.")
}