// Command mtls-ca is the tool for the client-certificate authority that
// gates the whole API at the TLS layer (nginx `ssl_verify_client`). Its
// private key must never leave whichever machine holds it — this
// deployment's choice is documented in docs/MTLS.md §"Où vit la CA". Same
// offline-tool philosophy as cmd/activate-sign, but a different trust root:
// that key authenticates an *account*, this one authenticates a *device*
// before a single HTTP request reaches nginx.
//
// Modes:
//
//	mtls-ca -genca
//	    Generates a fresh CA keypair + self-signed CA certificate (ECDSA
//	    P-256, 20 years), plus an empty issuance ledger. Writes the
//	    private key to ./mtls-ca.key (keep it safe — regenerating it
//	    invalidates every certificate issued so far) and the public
//	    certificate to ./mtls-ca.crt, which is the file nginx trusts as
//	    `ssl_client_certificate`.
//
//	mtls-ca -issue -name "device name" [-days 3650] [-ca-key path] [-ca-cert path]
//	    Generates a fresh client keypair + certificate signed by the CA,
//	    and records it (serial, name, issue date) in the ledger so it can
//	    be found again with -list and revoked with -revoke. Prints a
//	    single paste-able code: base64(client cert PEM + client key PEM).
//	    The blob contains a private key in cleartext — treat it as a
//	    secret in transit, same assumption as today's activation codes.
//
//	mtls-ca -list [-ledger path]
//	    Prints every issued certificate: serial, name, issue date, and
//	    revocation status.
//
//	mtls-ca -revoke -serial <hex> [-ca-key path] [-ca-cert path] [-ledger path] [-crl path]
//	    Marks one certificate revoked in the ledger and rewrites the CRL
//	    (X.509 certificate revocation list) that nginx checks via
//	    `ssl_crl`. Only *that* device loses access — every other
//	    certificate already issued keeps working, unlike regenerating the
//	    CA. Reload nginx after this (`nginx -t && systemctl reload nginx`).
//
//	mtls-ca -refresh-crl [-ca-key path] [-ca-cert path] [-ledger path] [-crl path]
//	    Rewrites the CRL from the ledger's current state without revoking
//	    anything. For bootstrapping mtls-ca.crl on a CA that predates -revoke
//	    (nginx's ssl_crl needs a file to exist, even with zero entries), or
//	    for renewing NextUpdate long before its 10-year horizon.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"math/big"
	"os"
	"sort"
	"strings"
	"time"
)

const (
	defaultCAKeyPath  = "mtls-ca.key"
	defaultCACertPath = "mtls-ca.crt"
	defaultLedgerPath = "issued.json"
	defaultCRLPath    = "mtls-ca.crl"
	caValidityYears   = 20
	// No automated renewal loop exists for this CRL, so it must not expire
	// on its own schedule — see docs/MTLS.md on why a short NextUpdate
	// would be a self-inflicted outage (nginx starts refusing *everyone*
	// once a referenced CRL goes stale, not just revoked certs).
	crlValidityYears = 10
)

func main() {
	genCA := flag.Bool("genca", false, "génère une nouvelle autorité de certification")
	issue := flag.Bool("issue", false, "émet un nouveau certificat client")
	list := flag.Bool("list", false, "liste les certificats émis")
	revoke := flag.Bool("revoke", false, "révoque un certificat")
	refreshCRL := flag.Bool("refresh-crl", false, "réécrit la CRL sans révoquer (première mise en place, ou renouvellement)")
	name := flag.String("name", "", "nom de l'appareil (mode -issue)")
	serial := flag.String("serial", "", "numéro de série à révoquer, hex (mode -revoke)")
	days := flag.Int("days", 3650, "durée de validité en jours (mode -issue)")
	caKeyPath := flag.String("ca-key", defaultCAKeyPath, "chemin de la clé privée de la CA")
	caCertPath := flag.String("ca-cert", defaultCACertPath, "chemin du certificat de la CA")
	ledgerPath := flag.String("ledger", defaultLedgerPath, "chemin du registre des certificats émis")
	crlPath := flag.String("crl", defaultCRLPath, "chemin de la liste de révocation (mode -revoke)")
	flag.Parse()

	switch {
	case *genCA:
		if err := runGenCA(*ledgerPath); err != nil {
			fail(err)
		}
	case *issue:
		if *name == "" {
			fail(fmt.Errorf("-name est requis en mode -issue"))
		}
		code, err := runIssue(*caKeyPath, *caCertPath, *ledgerPath, *name, *days)
		if err != nil {
			fail(err)
		}
		fmt.Println(code)
	case *list:
		if err := runList(*ledgerPath); err != nil {
			fail(err)
		}
	case *revoke:
		if *serial == "" {
			fail(fmt.Errorf("-serial est requis en mode -revoke (voir -list)"))
		}
		if err := runRevoke(*caKeyPath, *caCertPath, *ledgerPath, *crlPath, *serial); err != nil {
			fail(err)
		}
	case *refreshCRL:
		if err := runRefreshCRL(*caKeyPath, *caCertPath, *ledgerPath, *crlPath); err != nil {
			fail(err)
		}
	default:
		flag.Usage()
		os.Exit(1)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "erreur:", err)
	os.Exit(1)
}

// issuedRecord is one line of the ledger: enough to find a certificate again
// (by serial, the authoritative identifier — names may repeat) and to build
// a CRL from whichever records have RevokedAt set.
type issuedRecord struct {
	Serial    string     `json:"serial"`
	Name      string     `json:"name"`
	IssuedAt  time.Time  `json:"issuedAt"`
	RevokedAt *time.Time `json:"revokedAt,omitempty"`
}

type ledger struct {
	// CRL numbers must strictly increase (RFC 5280) across the CA's
	// lifetime, not just across revocations — tracked here rather than
	// derived from the CRL file itself so -revoke never has to parse its
	// own previous output back.
	NextCRLNumber int64          `json:"nextCrlNumber"`
	Certs         []issuedRecord `json:"certs"`
}

func loadLedger(path string) (ledger, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ledger{NextCRLNumber: 1}, nil
	}
	if err != nil {
		return ledger{}, fmt.Errorf("lecture du registre: %w", err)
	}
	var l ledger
	if err := json.Unmarshal(data, &l); err != nil {
		return ledger{}, fmt.Errorf("registre illisible (JSON invalide): %w", err)
	}
	if l.NextCRLNumber == 0 {
		l.NextCRLNumber = 1
	}
	return l, nil
}

func (l ledger) save(path string) error {
	data, err := json.MarshalIndent(l, "", "  ")
	if err != nil {
		return fmt.Errorf("encodage du registre: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("écriture du registre: %w", err)
	}
	return nil
}

func runGenCA(ledgerPath string) error {
	if _, err := os.Stat(defaultCAKeyPath); err == nil {
		return fmt.Errorf("%s existe déjà — le supprimer explicitement avant d'en régénérer une (une nouvelle CA invaliderait tous les certificats déjà émis)", defaultCAKeyPath)
	}

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("génération de la clé: %w", err)
	}

	serial, err := randomSerial()
	if err != nil {
		return fmt.Errorf("génération du numéro de série: %w", err)
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "Spotlab mTLS CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(caValidityYears, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &priv.PublicKey, priv)
	if err != nil {
		return fmt.Errorf("création du certificat: %w", err)
	}

	// PKCS8, not SEC1 (x509.MarshalECPrivateKey): Android's KeyFactory parses
	// PKCS8 EC keys natively, SEC1 would need a third-party ASN.1 parser to
	// unwrap on the client side.
	keyBytes, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return fmt.Errorf("encodage de la clé privée: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyBytes})
	if err := os.WriteFile(defaultCAKeyPath, keyPEM, 0o600); err != nil {
		return fmt.Errorf("écriture de la clé privée: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	if err := os.WriteFile(defaultCACertPath, certPEM, 0o644); err != nil {
		return fmt.Errorf("écriture du certificat: %w", err)
	}

	if err := (ledger{NextCRLNumber: 1}).save(ledgerPath); err != nil {
		return err
	}

	fmt.Printf("Clé privée CA : %s (à garder hors du dépôt, hors de tout poste qui n'a pas à l'avoir)\n", defaultCAKeyPath)
	fmt.Printf("Certificat CA : %s (à copier sur nginx, ssl_client_certificate)\n", defaultCACertPath)
	fmt.Printf("Registre : %s\n", ledgerPath)
	return nil
}

func runIssue(caKeyPath, caCertPath, ledgerPath, name string, days int) (string, error) {
	caCert, caPriv, err := loadCA(caKeyPath, caCertPath)
	if err != nil {
		return "", err
	}

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", fmt.Errorf("génération de la clé: %w", err)
	}

	serial, err := randomSerial()
	if err != nil {
		return "", fmt.Errorf("génération du numéro de série: %w", err)
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.AddDate(0, 0, days),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, caCert, &priv.PublicKey, caPriv)
	if err != nil {
		return "", fmt.Errorf("création du certificat: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	keyBytes, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return "", fmt.Errorf("encodage de la clé privée: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyBytes})

	l, err := loadLedger(ledgerPath)
	if err != nil {
		return "", err
	}
	l.Certs = append(l.Certs, issuedRecord{
		Serial:   serial.Text(16),
		Name:     name,
		IssuedAt: now,
	})
	if err := l.save(ledgerPath); err != nil {
		return "", err
	}

	blob := append(append([]byte{}, certPEM...), keyPEM...)
	return base64.StdEncoding.EncodeToString(blob), nil
}

func runList(ledgerPath string) error {
	l, err := loadLedger(ledgerPath)
	if err != nil {
		return err
	}
	if len(l.Certs) == 0 {
		fmt.Println("Aucun certificat émis (registre vide).")
		return nil
	}

	certs := append([]issuedRecord{}, l.Certs...)
	sort.Slice(certs, func(i, j int) bool { return certs[i].IssuedAt.Before(certs[j].IssuedAt) })

	for _, c := range certs {
		status := "actif"
		if c.RevokedAt != nil {
			status = "révoqué le " + c.RevokedAt.Format("2006-01-02")
		}
		fmt.Printf(
			"%-32s  %-24s  émis le %-10s  %s\n",
			c.Serial, c.Name, c.IssuedAt.Format("2006-01-02"), status,
		)
	}
	return nil
}

func runRevoke(caKeyPath, caCertPath, ledgerPath, crlPath, serialHex string) error {
	caCert, caPriv, err := loadCA(caKeyPath, caCertPath)
	if err != nil {
		return err
	}

	l, err := loadLedger(ledgerPath)
	if err != nil {
		return err
	}

	normalized := strings.ToLower(strings.TrimPrefix(serialHex, "0x"))
	index := -1
	for i, c := range l.Certs {
		if strings.ToLower(c.Serial) == normalized {
			index = i
			break
		}
	}
	if index == -1 {
		return fmt.Errorf("aucun certificat avec le numéro de série %q dans %s (voir -list)", serialHex, ledgerPath)
	}
	if l.Certs[index].RevokedAt != nil {
		return fmt.Errorf("le certificat %q (%s) est déjà révoqué depuis le %s",
			l.Certs[index].Serial, l.Certs[index].Name, l.Certs[index].RevokedAt.Format("2006-01-02"))
	}

	now := time.Now()
	l.Certs[index].RevokedAt = &now

	if err := writeCRL(caCert, caPriv, &l, ledgerPath, crlPath); err != nil {
		return err
	}

	fmt.Printf("Révoqué : %s (%s)\n", l.Certs[index].Serial, l.Certs[index].Name)
	fmt.Printf("CRL réécrite : %s — copier sur nginx (ssl_crl) puis recharger :\n", crlPath)
	fmt.Println("  nginx -t && systemctl reload nginx")
	return nil
}

// runRefreshCRL rewrites the CRL from the ledger's current state without
// revoking anything — for bootstrapping the file the first time (nginx's
// `ssl_crl` needs *something* to point at, even zero revocations) or for
// renewing NextUpdate well ahead of its 10-year horizon.
func runRefreshCRL(caKeyPath, caCertPath, ledgerPath, crlPath string) error {
	caCert, caPriv, err := loadCA(caKeyPath, caCertPath)
	if err != nil {
		return err
	}
	l, err := loadLedger(ledgerPath)
	if err != nil {
		return err
	}
	if err := writeCRL(caCert, caPriv, &l, ledgerPath, crlPath); err != nil {
		return err
	}
	fmt.Printf("CRL réécrite : %s — copier sur nginx (ssl_crl) puis recharger :\n", crlPath)
	fmt.Println("  nginx -t && systemctl reload nginx")
	return nil
}

func writeCRL(caCert *x509.Certificate, caPriv *ecdsa.PrivateKey, l *ledger, ledgerPath, crlPath string) error {
	crlDER, err := buildCRL(caCert, caPriv, *l)
	if err != nil {
		return err
	}
	l.NextCRLNumber++
	if err := l.save(ledgerPath); err != nil {
		return err
	}
	crlPEM := pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Bytes: crlDER})
	if err := os.WriteFile(crlPath, crlPEM, 0o644); err != nil {
		return fmt.Errorf("écriture de la CRL: %w", err)
	}
	return nil
}

func buildCRL(caCert *x509.Certificate, caPriv *ecdsa.PrivateKey, l ledger) ([]byte, error) {
	var revoked []x509.RevocationListEntry
	for _, c := range l.Certs {
		if c.RevokedAt == nil {
			continue
		}
		serial, ok := new(big.Int).SetString(c.Serial, 16)
		if !ok {
			return nil, fmt.Errorf("numéro de série illisible dans le registre: %q", c.Serial)
		}
		revoked = append(revoked, x509.RevocationListEntry{
			SerialNumber:   serial,
			RevocationTime: *c.RevokedAt,
		})
	}

	now := time.Now()
	template := &x509.RevocationList{
		Number:                    big.NewInt(l.NextCRLNumber),
		ThisUpdate:                now,
		NextUpdate:                now.AddDate(crlValidityYears, 0, 0),
		RevokedCertificateEntries: revoked,
	}
	return x509.CreateRevocationList(rand.Reader, template, caCert, caPriv)
}

func loadCA(keyPath, certPath string) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	keyBytes, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, nil, fmt.Errorf("lecture de la clé de la CA: %w", err)
	}
	keyBlock, _ := pem.Decode(keyBytes)
	if keyBlock == nil {
		return nil, nil, fmt.Errorf("clé de la CA illisible (PEM invalide)")
	}
	parsedKey, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing de la clé de la CA: %w", err)
	}
	priv, ok := parsedKey.(*ecdsa.PrivateKey)
	if !ok {
		return nil, nil, fmt.Errorf("clé de la CA: attendu ECDSA, trouvé %T", parsedKey)
	}

	certBytes, err := os.ReadFile(certPath)
	if err != nil {
		return nil, nil, fmt.Errorf("lecture du certificat de la CA: %w", err)
	}
	certBlock, _ := pem.Decode(certBytes)
	if certBlock == nil {
		return nil, nil, fmt.Errorf("certificat de la CA illisible (PEM invalide)")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parsing du certificat de la CA: %w", err)
	}

	return cert, priv, nil
}

func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	return rand.Int(rand.Reader, limit)
}
