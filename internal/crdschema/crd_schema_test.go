package crdschema

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"
)

// loadCRDFiles returns every YAML file in the given directory parsed as a CRD.
func loadCRDFiles(t *testing.T, dir string) map[string]*apiextv1.CustomResourceDefinition {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	out := map[string]*apiextv1.CustomResourceDefinition{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		p := filepath.Join(dir, e.Name())
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		var crd apiextv1.CustomResourceDefinition
		if err := yaml.Unmarshal(raw, &crd); err != nil {
			t.Fatalf("unmarshal %s: %v", p, err)
		}
		out[e.Name()] = &crd
	}
	return out
}

func TestCRDs_HavePhasePrinterColumn(t *testing.T) {
	crds := loadCRDFiles(t, "../../config/crd/bases")
	if len(crds) != 6 {
		t.Fatalf("expected 6 CRDs in config/crd/bases, found %d", len(crds))
	}
	for name, crd := range crds {
		t.Run(name, func(t *testing.T) {
			for _, v := range crd.Spec.Versions {
				var hasPhase bool
				for _, col := range v.AdditionalPrinterColumns {
					if col.Name == "Phase" && col.JSONPath == ".status.phase" {
						hasPhase = true
						break
					}
				}
				if !hasPhase {
					t.Errorf("CRD %s version %s missing 'Phase' printer column with JSONPath=.status.phase",
						crd.Name, v.Name)
				}
			}
		})
	}
}

func TestCRDs_PhaseEnumOnStatus(t *testing.T) {
	want := []string{"Pending", "Reconciling", "Ready", "Deleting", "Error"}
	sort.Strings(want)
	crds := loadCRDFiles(t, "../../config/crd/bases")
	for name, crd := range crds {
		t.Run(name, func(t *testing.T) {
			for _, v := range crd.Spec.Versions {
				schema := v.Schema.OpenAPIV3Schema
				status, ok := schema.Properties["status"]
				if !ok {
					t.Fatalf("CRD %s version %s has no status schema", crd.Name, v.Name)
				}
				phase, ok := status.Properties["phase"]
				if !ok {
					t.Fatalf("CRD %s version %s status.phase missing", crd.Name, v.Name)
				}
				got := []string{}
				for _, e := range phase.Enum {
					got = append(got, strings.Trim(string(e.Raw), `"`))
				}
				sort.Strings(got)
				if strings.Join(got, ",") != strings.Join(want, ",") {
					t.Errorf("CRD %s phase enum = %v, want %v", crd.Name, got, want)
				}
				if phase.Default == nil || strings.Trim(string(phase.Default.Raw), `"`) != "Pending" {
					t.Errorf("CRD %s phase default missing or != Pending", crd.Name)
				}
			}
		})
	}
}

func TestCRDs_ChartVendorParity(t *testing.T) {
	base := loadCRDFiles(t, "../../config/crd/bases")
	chart := loadCRDFiles(t, "../../chart/crds")
	if len(base) != len(chart) {
		t.Fatalf("base CRD count %d != chart CRD count %d — run `make sync-helm-crds`",
			len(base), len(chart))
	}
	for name, b := range base {
		c, ok := chart[name]
		if !ok {
			t.Errorf("chart/crds/%s missing — run `make sync-helm-crds`", name)
			continue
		}
		baseRaw, err := os.ReadFile(filepath.Join("../../config/crd/bases", name))
		if err != nil {
			t.Fatalf("read base %s: %v", name, err)
		}
		chartRaw, err := os.ReadFile(filepath.Join("../../chart/crds", name))
		if err != nil {
			t.Fatalf("read chart %s: %v", name, err)
		}
		if string(baseRaw) != string(chartRaw) {
			t.Errorf("CRD %s differs between config/crd/bases and chart/crds — run `make sync-helm-crds`", name)
		}
		_ = b
		_ = c
	}
}
