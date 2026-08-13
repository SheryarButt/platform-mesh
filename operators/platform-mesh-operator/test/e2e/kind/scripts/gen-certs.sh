#!/bin/bash

# Copyright The Platform Mesh Authors.
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

openssl genrsa -out webhook-config/ca.key 2048

openssl req -new -x509 -days 365 -key webhook-config/ca.key \
  -subj "/C=DE/CN=authz-server" -config webhook-config/openssl.conf \
  -out webhook-config/ca.crt

openssl req -newkey rsa:2048 -nodes -keyout webhook-config/tls.key \
  -subj "/C=DE/CN=authz-server" \
  -out webhook-config/tls.csr

  # -extfile <(printf "subjectAltName=DNS:host.containers.internal") \
openssl x509 -req \
  -days 365 \
  -extfile <(printf "subjectAltName=IP:10.96.86.219") \
  -in webhook-config/tls.csr \
  -CA webhook-config/ca.crt -CAkey webhook-config/ca.key -CAcreateserial \
  -out webhook-config/tls.crt

rm webhook-config/*.csr
