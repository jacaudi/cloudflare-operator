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

package reconcile

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestResolveInterval(t *testing.T) {
	fallback := 5 * time.Minute
	cases := []struct {
		name string
		in   *metav1.Duration
		want time.Duration
	}{
		{"nil uses fallback", nil, fallback},
		{"zero uses fallback", &metav1.Duration{Duration: 0}, fallback},
		{"negative uses fallback", &metav1.Duration{Duration: -time.Second}, fallback},
		{"positive overrides", &metav1.Duration{Duration: 90 * time.Second}, 90 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveInterval(tc.in, fallback); got != tc.want {
				t.Fatalf("ResolveInterval(%v, %v) = %v, want %v", tc.in, fallback, got, tc.want)
			}
		})
	}
}
