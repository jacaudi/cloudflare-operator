/*
Copyright (c) 2026 jacaudi

Licensed under the MIT License. See LICENSE in the project root for the
full license text.
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
