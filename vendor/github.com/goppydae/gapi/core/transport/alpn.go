package transport

// ALPNGapiQUIC is the ALPN protocol identifier for the kernel's QUIC
// transport. It mirrors the ecosystem ALPN registry as its governing
// contract (append-only, tombstoned): a collision is a review failure,
// not a runtime discovery. Consumers (the orchestrator's ALPN router)
// import this constant and never redefine the literal (GAPI-DIV-011).
const ALPNGapiQUIC = "gapi-quic"
