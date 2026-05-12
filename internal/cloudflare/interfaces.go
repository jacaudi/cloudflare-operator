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

// Package cloudflare interface declarations.
//
// This file is an append-only contract per Foundation §6.1.1. Spec 2 appends
// ZoneClient, DNSClient, RulesetClient, ZoneConfigClient. Spec 3 appends
// TunnelClient. Foundation creates the file empty so later specs have a
// canonical home for new interfaces; never restructure existing entries.

package cloudflare

// Sentinel — keep this file compiling when nothing else is appended yet.
type _interfacesContract struct{}
