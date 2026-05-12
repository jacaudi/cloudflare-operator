/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package bootstrap

import (
	"embed"
	"fmt"

	apiextv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
)

// Bundle identifies a controller bundle.
type Bundle string

const (
	BundleZone   Bundle = "zone"
	BundleTunnel Bundle = "tunnel"
)

//go:embed crds/*.yaml
var crdFS embed.FS

// bundleMembership lists the CRD filenames per bundle. Filenames must match
// what `make generate` produces.
var bundleMembership = map[Bundle][]string{
	BundleZone: {
		"crds/cloudflare.io_cloudflarezones.yaml",
		"crds/cloudflare.io_cloudflarezoneconfigs.yaml",
		"crds/cloudflare.io_cloudflarednsrecords.yaml",
		"crds/cloudflare.io_cloudflarerulesets.yaml",
	},
	BundleTunnel: {
		"crds/cloudflare.io_cloudflaretunnels.yaml",
	},
}

// BundleCRDs returns the parsed CustomResourceDefinitions for a bundle.
func BundleCRDs(b Bundle) ([]*apiextv1.CustomResourceDefinition, error) {
	files, ok := bundleMembership[b]
	if !ok {
		return nil, fmt.Errorf("unknown bundle %q", b)
	}
	out := make([]*apiextv1.CustomResourceDefinition, 0, len(files))
	for _, f := range files {
		raw, err := crdFS.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("read embedded %s: %w", f, err)
		}
		var crd apiextv1.CustomResourceDefinition
		if err := yaml.Unmarshal(raw, &crd); err != nil {
			return nil, fmt.Errorf("unmarshal %s: %w", f, err)
		}
		out = append(out, &crd)
	}
	return out, nil
}

// AllBundles returns every bundle the bootstrap reconciler knows about.
func AllBundles() []Bundle { return []Bundle{BundleZone, BundleTunnel} }
