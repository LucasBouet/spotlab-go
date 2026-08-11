// Command activate-sign is the offline tool for issuing activation codes.
// It is never deployed — the private key it uses must never leave the
// developer's machine. See docs/PLAN.md §5.
//
// Two modes:
//
//	activate-sign -genkey
//	    Generates a fresh RSA keypair. Writes the private key to
//	    ./activation_private.pem (gitignored — keep it safe, back it up
//	    somewhere that isn't this repo) and the public key to
//	    internal/auth/activation_public.pem, which the server embeds via
//	    go:embed and must be committed.
//
//	activate-sign -sign -sub "friend's name" [-exp 0] [-key path]
//	    Signs a new activation code for one person. Prints a single
//	    paste-able code: base64(payload) + "." + base64(signature).
//	    -exp is a Unix timestamp; 0 (the default) means "never expires".
package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"os"
	"time"
)

const (
	defaultPrivateKeyPath = "activation_private.pem"
	publicKeyPath         = "internal/auth/activation_public.pem"
	rsaBits               = 2048
)

// activationPayload is the JSON signed inside every code. Kept intentionally
// tiny: nothing here needs to round-trip through the app, only through the
// one-shot POST /api/activate call.
type activationPayload struct {
	Sub string `json:"sub"`
	IAT int64  `json:"iat"`
	Exp int64  `json:"exp"`
}

func main() {
	genKey := flag.Bool("genkey", false, "génère une nouvelle paire de clés RSA")
	sign := flag.Bool("sign", false, "signe un nouveau code d'activation")
	sub := flag.String("sub", "", "nom de la personne invitée (mode -sign)")
	exp := flag.Int64("exp", 0, "expiration Unix, 0 = jamais (mode -sign)")
	keyPath := flag.String("key", defaultPrivateKeyPath, "chemin de la clé privée (mode -sign)")
	flag.Parse()

	switch {
	case *genKey:
		if err := runGenKey(); err != nil {
			fmt.Fprintln(os.Stderr, "erreur:", err)
			os.Exit(1)
		}
	case *sign:
		if *sub == "" {
			fmt.Fprintln(os.Stderr, "erreur: -sub est requis en mode -sign")
			os.Exit(1)
		}
		code, err := runSign(*keyPath, *sub, *exp)
		if err != nil {
			fmt.Fprintln(os.Stderr, "erreur:", err)
			os.Exit(1)
		}
		fmt.Println(code)
	default:
		flag.Usage()
		os.Exit(1)
	}
}

func runGenKey() error {
	if _, err := os.Stat(defaultPrivateKeyPath); err == nil {
		return fmt.Errorf("%s existe déjà — le supprimer explicitement avant d'en régénérer une (une nouvelle clé invaliderait tous les codes déjà signés)", defaultPrivateKeyPath)
	}

	priv, err := rsa.GenerateKey(rand.Reader, rsaBits)
	if err != nil {
		return fmt.Errorf("génération de la clé: %w", err)
	}

	privBytes := x509.MarshalPKCS1PrivateKey(priv)
	privPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: privBytes})
	if err := os.WriteFile(defaultPrivateKeyPath, privPEM, 0o600); err != nil {
		return fmt.Errorf("écriture de la clé privée: %w", err)
	}

	pubBytes, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		return fmt.Errorf("encodage de la clé publique: %w", err)
	}
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubBytes})
	if err := os.WriteFile(publicKeyPath, pubPEM, 0o644); err != nil {
		return fmt.Errorf("écriture de la clé publique: %w", err)
	}

	fmt.Printf("Clé privée : %s (à garder hors du dépôt, hors du serveur)\n", defaultPrivateKeyPath)
	fmt.Printf("Clé publique : %s (à committer, embarquée dans le serveur)\n", publicKeyPath)
	return nil
}

func runSign(keyPath, sub string, exp int64) (string, error) {
	keyBytes, err := os.ReadFile(keyPath)
	if err != nil {
		return "", fmt.Errorf("lecture de la clé privée: %w", err)
	}
	block, _ := pem.Decode(keyBytes)
	if block == nil {
		return "", fmt.Errorf("clé privée illisible (PEM invalide)")
	}
	priv, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("parsing de la clé privée: %w", err)
	}

	payload := activationPayload{Sub: sub, IAT: time.Now().Unix(), Exp: exp}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encodage du payload: %w", err)
	}

	hash := sha256.Sum256(payloadJSON)
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, hash[:])
	if err != nil {
		return "", fmt.Errorf("signature: %w", err)
	}

	return base64.RawURLEncoding.EncodeToString(payloadJSON) + "." +
		base64.RawURLEncoding.EncodeToString(sig), nil
}
