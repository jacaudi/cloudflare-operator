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

// Package cloudflare wraps the cloudflare-go SDK with the operator's
// credential resolution, error classification, and (SDK-built-in) retry
// semantics.
//
// interfaces.go is append-only across spec increments: spec 2 (zone bundle)
// shipped DNSClient / ZoneClient / RulesetClient / ZoneConfigClient; spec 3
// (tunnel bundle) appended TunnelClient. Future specs append; never remove
// or rename a published interface.
package cloudflare
