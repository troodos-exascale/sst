# Copyright 2026 Peter J. Braam
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# CANONICAL_NAME: alpine-3.23.3-test-alpine-net
# LAST_BUILD: 20260321-192338
FROM alpine:latest
NAME: test-alpine-net

RUN: |
  apk update && apk add curl bind-tools

# EMIT: container prevents the generation of a .raw disk, 
# triggering the physical ZFS rootfs promotion in FinalizeVM
EMIT: container
