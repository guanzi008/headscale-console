package idp

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"os"

	"github.com/golang-jwt/jwt/v5"
)

type signer struct {
	key *rsa.PrivateKey
	kid string
}

func loadOrCreateSigner(path string) (*signer, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		block, _ := pem.Decode(data)
		if block == nil {
			return nil, fmt.Errorf("decode OIDC signing key: no PEM block")
		}
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse OIDC signing key: %w", err)
		}
		privateKey, ok := key.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("OIDC signing key is not RSA")
		}
		return newSigner(privateKey), nil
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read OIDC signing key: %w", err)
	}

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, fmt.Errorf("generate OIDC signing key: %w", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return nil, fmt.Errorf("encode OIDC signing key: %w", err)
	}
	encoded := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		return nil, fmt.Errorf("write OIDC signing key: %w", err)
	}
	return newSigner(privateKey), nil
}

func newSigner(key *rsa.PrivateKey) *signer {
	der := x509.MarshalPKCS1PublicKey(&key.PublicKey)
	digest := sha256.Sum256(der)
	return &signer{
		key: key,
		kid: base64.RawURLEncoding.EncodeToString(digest[:16]),
	}
}

func (s *signer) sign(claims jwt.MapClaims) (string, error) {
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = s.kid
	return token.SignedString(s.key)
}

func (s *signer) jwk() map[string]any {
	public := s.key.PublicKey
	return map[string]any{
		"kty": "RSA",
		"use": "sig",
		"alg": "RS256",
		"kid": s.kid,
		"n":   base64.RawURLEncoding.EncodeToString(public.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(public.E)).Bytes()),
	}
}
