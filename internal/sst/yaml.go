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

package sst

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"sigs.k8s.io/yaml"
)

//
// ======================================================
// Diagnostics (minimal, aggregatable)
// ======================================================
//
// This is intentionally small. It is the structured payload we can:
//  1) return to factories/remote callers
//  2) optionally emit to logx (Diagnose if available)
//  3) render as an errorf-style message at the CLI boundary
//

type YAMLDiagnostic struct {
	Code    string
	Message string
	Path    string
	Schema  string
	Meta    map[string]any
}

type YAMLErrors struct {
	Items []YAMLDiagnostic
}

func (d *YAMLErrors) Add(diag YAMLDiagnostic) {
	if diag.Meta == nil {
		diag.Meta = map[string]any{}
	}
	d.Items = append(d.Items, diag)
}

func (d *YAMLErrors) HasAny() bool { return d != nil && len(d.Items) > 0 }

func (d *YAMLErrors) Error() string {
	if d == nil || len(d.Items) == 0 {
		return ""
	}
	// interactive-friendly summary
	if len(d.Items) == 1 {
		return d.Items[0].Message
	}
	return fmt.Sprintf("%d validation/load error(s) (first: %s)", len(d.Items), d.Items[0].Message)
}

func (d *YAMLErrors) FirstCode() string {
	if d == nil || len(d.Items) == 0 {
		return ""
	}
	return d.Items[0].Code
}

//
// ======================================================
// YamlObject (your “YAML object”)
// ======================================================
//
// Holds identity + location + typed payload.
// The payload is a protobuf-generated message.
//
// Schema is a stable string you control, e.g.
//   "sst.storage.v1.StorageLayout"
// or
//   "sst.runtime.v1.RuntimeConfig"
//
// Kind/APIVersion are optional metadata; if you model them in the proto,
// they will be validated by your own ValidateFn (or by generated validators).
//

type YamlObject struct {
	Path       string
	Schema     string
	APIVersion string
	Kind       string

	Msg proto.Message

	// Optional hooks
	ValidateFn  func(ctx context.Context, y *YamlObject) *YAMLErrors
	NormalizeFn func(ctx context.Context, y *YamlObject)
}

func (y *YamlObject) ensure() error {
	if y == nil {
		return errors.New("YamlObject is nil")
	}
	if strings.TrimSpace(y.Path) == "" {
		return errors.New("YamlObject.Path is empty")
	}
	if y.Msg == nil {
		return errors.New("YamlObject.Msg is nil")
	}
	if strings.TrimSpace(y.Schema) == "" {
		// Schema is strongly recommended; keep it required to avoid chaos.
		return errors.New("YamlObject.Schema is empty")
	}
	return nil
}

//
// ======================================================
// Load / Validate / Save
// ======================================================

func (y *YamlObject) Load(ctx context.Context, log *Logx) *YAMLErrors {
	if err := y.ensure(); err != nil {
		return y.diag(ctx, log, "YAML_OBJ_INVALID", err.Error(), nil)
	}

	raw, err := os.ReadFile(y.Path)
	if err != nil {
		return y.diag(ctx, log, "YAML_READ_FAILED", fmt.Sprintf("read %s: %v", y.Path, err), map[string]any{
			"path": y.Path,
		})
	}

	// YAML -> JSON
	j, err := yaml.YAMLToJSON(raw)
	if err != nil {
		return y.diag(ctx, log, "YAML_PARSE_FAILED", fmt.Sprintf("parse yaml %s: %v", y.Path, err), map[string]any{
			"path": y.Path,
		})
	}

	// JSON -> protobuf (STRICT: unknown fields are NOT discarded)
	uopts := protojson.UnmarshalOptions{
		DiscardUnknown: false,
	}

	if err := uopts.Unmarshal(j, y.Msg); err != nil {
		return y.diag(ctx, log, "YAML_DECODE_FAILED", fmt.Sprintf("decode %s as %s: %v", y.Path, y.Schema, err), map[string]any{
			"path":   y.Path,
			"schema": y.Schema,
		})
	}

	// Normalize (optional) after load
	if y.NormalizeFn != nil {
		y.NormalizeFn(ctx, y)
	}

	// Validate after load
	if diags := y.Validate(ctx, log); diags != nil && diags.HasAny() {
		return diags
	}

	return nil
}

func (y *YamlObject) Validate(ctx context.Context, log *Logx) *YAMLErrors {
	if err := y.ensure(); err != nil {
		return y.diag(ctx, log, "YAML_OBJ_INVALID", err.Error(), nil)
	}

	// Optional custom validator
	if y.ValidateFn != nil {
		if diags := y.ValidateFn(ctx, y); diags != nil && diags.HasAny() {
			y.emitAll(ctx, log, diags, "YAML_VALIDATE_FAILED")
			return diags
		}
		return nil
	}

	// Optional generated validator compatibility:
	// - protovalidate typically uses a separate validator object
	// - PGV sometimes generates Validate() error
	// We support the common “Validate() error” convention here.
	type hasValidate interface{ Validate() error }
	if v, ok := y.Msg.(hasValidate); ok {
		if err := v.Validate(); err != nil {
			return y.diag(ctx, log, "YAML_VALIDATE_FAILED", fmt.Sprintf("%s invalid: %v", y.Path, err), map[string]any{
				"path":   y.Path,
				"schema": y.Schema,
			})
		}
	}

	// If no ValidateFn and no generated validator: validation is a no-op for now.
	return nil
}

func (y *YamlObject) Save(ctx context.Context, log *Logx) *YAMLErrors {
	if err := y.ensure(); err != nil {
		return y.diag(ctx, log, "YAML_OBJ_INVALID", err.Error(), nil)
	}

	mopts := protojson.MarshalOptions{
		EmitUnpopulated: true,
		UseProtoNames:   true, // stable / matches schema fields
		Indent:          "  ",
	}

	j, err := mopts.Marshal(y.Msg)
	if err != nil {
		return y.diag(ctx, log, "YAML_ENCODE_FAILED", fmt.Sprintf("encode %s as %s: %v", y.Path, y.Schema, err), map[string]any{
			"path":   y.Path,
			"schema": y.Schema,
		})
	}

	// JSON -> YAML
	yb, err := yaml.JSONToYAML(j)
	if err != nil {
		return y.diag(ctx, log, "YAML_SERIALIZE_FAILED", fmt.Sprintf("serialize %s as yaml: %v", y.Path, err), map[string]any{
			"path": y.Path,
		})
	}

	if err := os.WriteFile(y.Path, yb, 0o644); err != nil {
		return y.diag(ctx, log, "YAML_WRITE_FAILED", fmt.Sprintf("write %s: %v", y.Path, err), map[string]any{
			"path": y.Path,
		})
	}

	return nil
}

//
// ======================================================
// Emission helpers
// ======================================================

func (y *YamlObject) diag(ctx context.Context, log *Logx, code, msg string, meta map[string]any) *YAMLErrors {
	d := &YAMLErrors{}
	d.Add(YAMLDiagnostic{
		Code:    code,
		Message: msg,
		Path:    y.Path,
		Schema:  y.Schema,
		Meta:    meta,
	})
	y.emitAll(ctx, log, d, code)
	return d
}

func (y *YamlObject) emitAll(ctx context.Context, log *Logx, ds *YAMLErrors, defaultCode string) {
	if ds == nil || len(ds.Items) == 0 {
		return
	}

	for _, it := range ds.Items {
		code := strings.TrimSpace(it.Code)
		if code == "" {
			code = defaultCode
		}

		kv := []any{
			"schema", it.Schema,
			"path", it.Path,
		}
		if y.Kind != "" {
			kv = append(kv, "kind", y.Kind)
		}
		if y.APIVersion != "" {
			kv = append(kv, "api_version", y.APIVersion)
		}
		if len(it.Meta) > 0 {
			kv = append(kv, "meta", it.Meta)
		}

		log.Diagnose(ctx, code, it.Message, kv...)
	}
}
