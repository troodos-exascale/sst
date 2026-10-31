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

FROM ubuntu-24.04-server-cloudimg-arm64
NAME: ubuntu-node

ENV INST_HOSTNAME=ubuntu-node
ENV INST_DOMAIN=local
ENV INST_CPUS=4
ENV INST_MEM=4096

# 1. Network configuration (DHCP + mDNS Broadcast)
INCLUDE runtime/02-network.dockerfile
INCLUDE runtime/021-mdns.dockerfile

# 2. Identity and Access
ENV INST_USER=braam

# Recommended: Place these files in examples/runtimes/secrets/ so the VM can read them!
ENV INST_PUBKEY_HOSTFILE=$HOME/secrets/ssh_pubkey
INCLUDE runtime/01-user.dockerfile

INCLUDE runtime/011-sudo.dockerfile

ENV INST_CONSOLE_PASS_HOSTFILE=$HOME/secrets/password
INCLUDE runtime/012-console-password.dockerfile

# 3. Storage and Hardening
ENV INST_DEVICES="data:10G,wal:2G"
INCLUDE runtime/04-storage.dockerfile
INCLUDE runtime/03-hardening.dockerfile


# Tell the engine the exact physical name of the base cloud image file
ENV INST_BASE_DISK="disk.raw"

# Use your existing templates/emit/raw.dockerfile
EMIT: runtime
