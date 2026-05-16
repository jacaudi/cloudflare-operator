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

package cloudflare

// Compile-time interface-satisfaction assertions. These assignments fail to
// compile the moment a concrete type stops implementing its interface — which
// is the real contract we need to protect, not a runtime check.
var (
	_ DNSClient        = (*dnsClient)(nil)
	_ ZoneClient       = (*zoneClient)(nil)
	_ ZoneConfigClient = (*zoneConfigClient)(nil)
	_ RulesetClient    = (*rulesetClient)(nil)
	_ TunnelClient     = (*tunnelClient)(nil)
)
