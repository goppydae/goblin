package cli

import (
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/goppydae/gapi/core/crypto"
	"github.com/spf13/cobra"
)

var signCmd = &cobra.Command{
	Use:   "sign <file> --key <private.pem>",
	Short: "Sign a file and produce a detached .sig",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		file := args[0]
		keyPath, _ := cmd.Flags().GetString("key")
		if keyPath == "" {
			return fmt.Errorf("private key required (--key)")
		}

		key, err := crypto.LoadPrivate(keyPath)
		if err != nil {
			return fmt.Errorf("load key: %w", err)
		}

		// Hash file
		hash, err := crypto.HashFile(file)
		if err != nil {
			return fmt.Errorf("hash file: %w", err)
		}

		// Sign hash
		sig := key.Sign([]byte(hash))
		sigHex := hex.EncodeToString(sig)

		// Write .sig
		sigPath := file + ".sig"
		if err := os.WriteFile(sigPath, []byte(sigHex), 0644); err != nil {
			return err
		}

		fmt.Printf("Signed %s -> %s\n", file, sigPath)
		return nil
	},
}

var keygenCmd = &cobra.Command{
	Use:   "keygen --out <path>",
	Short: "Generate a new Ed25519 keypair",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		prefix, _ := cmd.Flags().GetString("out")
		if prefix == "" {
			return fmt.Errorf("output prefix required (--out)")
		}

		key, err := crypto.GenerateKey()
		if err != nil {
			return err
		}

		if err := key.SavePrivate(prefix + ".pem"); err != nil {
			return err
		}
		if err := key.SavePublic(prefix + ".pub.hex"); err != nil {
			return err
		}

		fmt.Printf("Generated keypair:\n  Private: %s.pem\n  Public:  %s.pub.hex\n", prefix, prefix)
		return nil
	},
}

var verifyCmd = &cobra.Command{
	Use:   "verify <file> --pub <public.hex>",
	Short: "Verify a file against its .sig",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		file := args[0]
		keyPath, _ := cmd.Flags().GetString("pub")
		if keyPath == "" {
			return fmt.Errorf("public key required (--pub)")
		}

		pub, err := crypto.LoadPublic(keyPath)
		if err != nil {
			return fmt.Errorf("load pub key: %w", err)
		}

		// Read signature
		sigHexBytes, err := os.ReadFile(file + ".sig")
		if err != nil {
			return fmt.Errorf("read signature: %w", err)
		}
		sig, err := hex.DecodeString(strings.TrimSpace(string(sigHexBytes)))
		if err != nil {
			return fmt.Errorf("decode signature: %w", err)
		}

		// Hash file
		hash, err := crypto.HashFile(file)
		if err != nil {
			return fmt.Errorf("hash file: %w", err)
		}

		valid := crypto.Verify(pub, []byte(hash), sig)
		if valid {
			fmt.Println("OK")
			return nil
		}
		return fmt.Errorf("signature verification FAILED")
	},
}

var ageKeygenCmd = &cobra.Command{
	Use:   "age-keygen",
	Short: "Generate a new AGE identity",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		id, err := crypto.GenerateAgeIdentity()
		if err != nil {
			return err
		}
		// Print to stdout
		fmt.Printf("# public key: %s\n%s\n",
			id.Recipient().String(),
			id.String(),
		)
		return nil
	},
}

var encryptCmd = &cobra.Command{
	Use:   "encrypt --recipient <pubkey>",
	Short: "Encrypt data from stdin to stdout (AGE)",
	RunE: func(cmd *cobra.Command, args []string) error {
		recipients, _ := cmd.Flags().GetStringSlice("recipient")
		if len(recipients) == 0 {
			return fmt.Errorf("at least one recipient required (--recipient)")
		}

		plain, err := io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("read stdin: %w", err)
		}

		cipher, err := crypto.EncryptAge(recipients, plain)
		if err != nil {
			return err
		}

		if _, err := os.Stdout.Write(cipher); err != nil {
			return err
		}
		return nil
	},
}

var decryptCmd = &cobra.Command{
	Use:   "decrypt --identity <key.txt>",
	Short: "Decrypt data from stdin to stdout (AGE)",
	RunE: func(cmd *cobra.Command, args []string) error {
		identity, _ := cmd.Flags().GetString("identity")
		if identity == "" {
			return fmt.Errorf("identity file required (--identity)")
		}

		cipher, err := io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("read stdin: %w", err)
		}

		plain, err := crypto.DecryptAge(identity, cipher)
		if err != nil {
			return err
		}

		if _, err := os.Stdout.Write(plain); err != nil {
			return err
		}
		return nil
	},
}

func init() {
	keygenCmd.Flags().String("out", "id_ed25519", "Output prefix")
	signCmd.Flags().String("key", "", "Path to private key (PEM)")
	verifyCmd.Flags().String("pub", "", "Path to public key (hex)")

	rootCmd.AddCommand(signCmd)
	rootCmd.AddCommand(keygenCmd)
	rootCmd.AddCommand(verifyCmd)

	rootCmd.AddCommand(ageKeygenCmd)

	encryptCmd.Flags().StringSliceP("recipient", "r", nil, "Recipient public key(s)")
	rootCmd.AddCommand(encryptCmd)

	decryptCmd.Flags().StringP("identity", "i", "", "Identity file")
	rootCmd.AddCommand(decryptCmd)
}
