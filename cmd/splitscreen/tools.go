package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/avarant/splitscreen/config"
	"github.com/avarant/splitscreen/internal/harness"
	"github.com/avarant/splitscreen/protocol"
)

// ---------------------------------------------------------------------------
// enroll
// ---------------------------------------------------------------------------

func enrollCmd() *cobra.Command {
	var runnerName string

	cmd := &cobra.Command{
		Use:   "enroll <runner>",
		Short: "Generate an enrollment token for a runner",
		Long: `Prints a fresh enrollment token and the secret name the gateway will look
it up under.

Store the value in the gateway's secret backend, and deliver it to the runner by
a path that does not put it in argv or a shell history — a file, or the
environment.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runnerName = args[0]
			if !protocol.ValidSlug(runnerName) {
				return fmt.Errorf("splitscreen: %q is not a valid runner name (lowercase letters, digits, dashes)", runnerName)
			}
			var raw [32]byte
			if _, err := rand.Read(raw[:]); err != nil {
				return err
			}
			token := base64.RawURLEncoding.EncodeToString(raw[:])

			fmt.Printf("runner:        %s\n", runnerName)
			fmt.Printf("secret name:   runner-%s\n", runnerName)
			fmt.Printf("token:         %s\n\n", token)
			fmt.Printf("On the gateway, with a directory secret backend:\n")
			fmt.Printf("  printf %%s '%s' > $SECRETS_DIR/runner-%s && chmod 600 $SECRETS_DIR/runner-%s\n\n",
				token, runnerName, runnerName)
			fmt.Printf("On the runner:\n")
			fmt.Printf("  printf %%s '%s' > /etc/splitscreen/token && chmod 600 /etc/splitscreen/token\n",
				token)
			fmt.Printf("  splitscreen runner --name %s --token-file /etc/splitscreen/token ...\n", runnerName)
			return nil
		},
	}
	return cmd
}

// ---------------------------------------------------------------------------
// config check
// ---------------------------------------------------------------------------

func configCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect configuration",
	}

	var cfgPath string
	check := &cobra.Command{
		Use:   "check",
		Short: "Validate a configuration file",
		Long: `Validates a configuration file and reports every problem at once.

This is the same code path the gateway uses on load and on SIGHUP, so a config
that passes here will not be rejected at reload.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(cfgPath)
			if err != nil {
				return err
			}
			fmt.Printf("%s is valid.\n\n", cfgPath)

			names := make([]string, 0, len(cfg.Runners))
			for n := range cfg.Runners {
				names = append(names, n)
			}
			sort.Strings(names)

			fmt.Printf("Runners (%d):\n", len(names))
			for _, n := range names {
				r := cfg.Runners[n]
				fmt.Printf("  %-20s harness=%-12s cwd=%s idle=%s\n", n, r.Harness, r.Cwd, r.Idle)
			}

			fmt.Printf("\nRoutes (%d):\n", len(cfg.Routes))
			for _, r := range cfg.Routes {
				target := r.Channel
				if r.DM {
					target = "<direct messages>"
				}
				fmt.Printf("  %-20s -> %s\n", target, r.Runner)
			}

			if proxied := cfg.ProxiedServers(); len(proxied) > 0 {
				sort.Strings(proxied)
				fmt.Printf("\nProxied MCP servers: %v\n", proxied)
			}

			refs := cfg.SecretRefs()
			sort.Strings(refs)
			fmt.Printf("\nSecrets this config expects (%d): %v\n", len(refs), refs)
			fmt.Printf("Harness adapters available: %v\n", harness.Names())
			return nil
		},
	}
	check.Flags().StringVarP(&cfgPath, "config", "c", "splitscreen.yaml", "path to the configuration file")

	cmd.AddCommand(check)
	return cmd
}

// ---------------------------------------------------------------------------
// cert
// ---------------------------------------------------------------------------

func certCmd() *cobra.Command {
	var (
		certPath, keyPath string
		hosts             []string
		days              int
	)

	cmd := &cobra.Command{
		Use:   "cert",
		Short: "Generate a self-signed certificate for the gateway",
		Long: `Generates a self-signed certificate and prints its fingerprint.

Public CAs will not issue for a private address, and a private CA is
disproportionate for a small fleet. Runners pin the fingerprint printed here,
which is a stronger check than chain validation for a two-node deployment.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if len(hosts) == 0 {
				return fmt.Errorf("splitscreen: at least one --host is required")
			}
			key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if err != nil {
				return err
			}
			serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
			if err != nil {
				return err
			}
			tmpl := x509.Certificate{
				SerialNumber:          serial,
				Subject:               pkix.Name{CommonName: hosts[0], Organization: []string{"Splitscreen"}},
				NotBefore:             time.Now().Add(-time.Hour),
				NotAfter:              time.Now().AddDate(0, 0, days),
				KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
				ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
				BasicConstraintsValid: true,
				IsCA:                  true,
			}
			for _, h := range hosts {
				if ip := net.ParseIP(h); ip != nil {
					tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
				} else {
					tmpl.DNSNames = append(tmpl.DNSNames, h)
				}
			}

			der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
			if err != nil {
				return err
			}
			certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
			keyDER, err := x509.MarshalECPrivateKey(key)
			if err != nil {
				return err
			}
			keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

			if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
				return err
			}
			if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
				return err
			}

			sum := sha256.Sum256(der)
			fmt.Printf("certificate: %s\n", certPath)
			fmt.Printf("private key: %s (mode 0600)\n", keyPath)
			fmt.Printf("hosts:       %v\n", hosts)
			fmt.Printf("expires:     %s\n\n", tmpl.NotAfter.Format(time.RFC3339))
			fmt.Printf("Pin this on every runner:\n")
			fmt.Printf("  --fingerprint sha256:%s\n", hex.EncodeToString(sum[:]))
			return nil
		},
	}

	f := cmd.Flags()
	f.StringVar(&certPath, "cert", "gateway.crt", "certificate output path")
	f.StringVar(&keyPath, "key", "gateway.key", "private key output path")
	f.StringSliceVar(&hosts, "host", nil, "DNS name or IP the gateway is reached at (repeatable)")
	f.IntVar(&days, "days", 825, "validity in days")
	return cmd
}
