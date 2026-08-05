// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package transport

// ALPNGapiQUIC is the ALPN protocol identifier for the kernel's QUIC
// transport. It mirrors the ecosystem ALPN registry as its governing
// contract (append-only, tombstoned): a collision is a review failure,
// not a runtime discovery. Consumers (the orchestrator's ALPN router)
// import this constant and never redefine the literal (GAPI-DIV-011).
const ALPNGapiQUIC = "gapi-quic"
