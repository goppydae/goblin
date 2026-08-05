// Copyright (c) 2025 Steven Verhelle (enqack)
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.
//
// SPDX-License-Identifier: MPL-2.0

package crypto

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"reflect"
)

type PEMNS struct{}

func (PEMNS) EncodePrivateKey(key any) ([]byte, error) {
	switch k := key.(type) {
	case *rsa.PrivateKey:
		b := x509.MarshalPKCS1PrivateKey(k)
		return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: b}), nil
	case ed25519.PrivateKey:
		b, err := x509.MarshalPKCS8PrivateKey(k)
		if err != nil {
			return nil, err
		}
		return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: b}), nil
	case *ecdsa.PrivateKey:
		b, err := x509.MarshalECPrivateKey(k)
		if err != nil {
			return nil, err
		}
		return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: b}), nil
	default:
		return nil, errors.New("unsupported private key type")
	}
}

func (PEMNS) EncodePublicKey(key any) ([]byte, error) {
	b, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: b}), nil
}

func (PEMNS) ParsePrivateKey(data []byte) (any, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("invalid PEM block")
	}

	switch block.Type {
	case "RSA PRIVATE KEY":
		return x509.ParsePKCS1PrivateKey(block.Bytes)
	case "EC PRIVATE KEY":
		return x509.ParseECPrivateKey(block.Bytes)
	case "PRIVATE KEY":
		return x509.ParsePKCS8PrivateKey(block.Bytes)
	default:
		return nil, fmt.Errorf("unknown private key type: %s", block.Type)
	}
}

func (PEMNS) ParsePublicKey(data []byte) (any, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("invalid PEM block")
	}
	return x509.ParsePKIXPublicKey(block.Bytes)
}

func (PEMNS) FingerprintPublicKey(key any) (string, error) {
	b, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

func (PEMNS) FormatFingerprint(key any, truncateTo int) (string, error) {
	fp, err := PEM.FingerprintPublicKey(key)
	if err != nil {
		return "", err
	}
	short := fp
	if truncateTo > 0 && len(fp) > truncateTo {
		short = fp[:truncateTo]
	}
	var prefix string
	switch k := key.(type) {
	case ed25519.PublicKey:
		prefix = "ed25519"
	case *rsa.PublicKey:
		prefix = fmt.Sprintf("rsa%d", k.N.BitLen())
	case *ecdsa.PublicKey:
		prefix = "ecdsa"
	default:
		prefix = reflect.TypeOf(key).String()
	}
	return fmt.Sprintf("%s:%s", prefix, short), nil
}

var PEM = PEMNS{}
