package plugindev

import (
	"bytes"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"os"

	pluginsdk "github.com/Tencent/WeKnora/sdk/plugin/go"
)

type SignatureResult struct {
	Signature string
	PublicKey string
}

func SignManifest(manifestPath, privateKeyPath string) (SignatureResult, error) {
	if privateKeyPath == "" {
		return SignatureResult{}, errors.New("private key path is required")
	}
	manifest, err := ValidateManifest(manifestPath)
	if err != nil {
		return SignatureResult{}, err
	}
	privateKey, err := readEd25519PrivateKey(privateKeyPath)
	if err != nil {
		return SignatureResult{}, err
	}
	payload, err := pluginsdk.SupplyChainSigningPayload(
		manifest.Metadata.ID,
		manifest.Metadata.Version,
		manifest.Spec.Image,
		manifest.Spec.SupplyChain.Publisher,
		manifest.Spec.SupplyChain.SourceRepository,
		manifest.Spec.SupplyChain.ImageDigest,
	)
	if err != nil {
		return SignatureResult{}, fmt.Errorf("build signing payload: %w", err)
	}
	publicKey, ok := privateKey.Public().(ed25519.PublicKey)
	if !ok {
		return SignatureResult{}, errors.New("private key does not contain an Ed25519 public key")
	}
	return SignatureResult{
		Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload)),
		PublicKey: base64.StdEncoding.EncodeToString(publicKey),
	}, nil
}

func readEd25519PrivateKey(path string) (ed25519.PrivateKey, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read private key: %w", err)
	}
	block, rest := pem.Decode(contents)
	if block == nil || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("private key must contain exactly one PEM block")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse PKCS#8 private key: %w", err)
	}
	privateKey, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("private key must use Ed25519")
	}
	return privateKey, nil
}
