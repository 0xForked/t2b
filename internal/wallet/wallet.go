// Package wallet generates and stores an encrypted Solana keypair.
//
// A Solana keypair is just an ed25519 keypair; the "private key" users paste
// around is the 64-byte seed||pubkey, base58-encoded. We keep that format so
// keys generated here import cleanly into Phantom/solana-cli et al.
package wallet

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mr-tron/base58"
	"golang.org/x/crypto/nacl/secretbox"
	"golang.org/x/crypto/scrypt"
)

const (
	scryptN = 1 << 15
	scryptR = 8
	scryptP = 1
	keyLen  = 32
	saltLen = 16
)

// Wallet holds the decrypted keypair in memory only.
type Wallet struct {
	Private ed25519.PrivateKey
	Public  ed25519.PublicKey
}

// PublicBase58 is the address as shown on-chain / in explorers.
func (w *Wallet) PublicBase58() string {
	return base58.Encode(w.Public)
}

// PrivateBase58 is the 64-byte seed||pubkey blob, base58 — the format
// Phantom/solana-cli expect for import/export.
func (w *Wallet) PrivateBase58() string {
	return base58.Encode(w.Private)
}

// Generate creates a fresh ed25519 keypair.
func Generate() (*Wallet, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return &Wallet{Private: priv, Public: pub}, nil
}

// ImportBase58 loads a keypair from a base58-encoded 64-byte private key
// (the format solana-cli / Phantom export).
func ImportBase58(s string) (*Wallet, error) {
	b, err := base58.Decode(s)
	if err != nil {
		return nil, fmt.Errorf("invalid base58: %w", err)
	}
	if len(b) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("expected %d-byte private key, got %d", ed25519.PrivateKeySize, len(b))
	}
	priv := ed25519.PrivateKey(b)
	return &Wallet{Private: priv, Public: priv.Public().(ed25519.PublicKey)}, nil
}

type encryptedFile struct {
	Salt  string `json:"salt"`  // base58
	Nonce string `json:"nonce"` // base58
	Box   string `json:"box"`   // base58 sealed private key
}

func filePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".t2b")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, "wallet.enc"), nil
}

// Save encrypts the private key with a passphrase (scrypt KDF + nacl
// secretbox) and writes it to ~/.t2b/wallet.enc.
func (w *Wallet) Save(passphrase string) error {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	key, err := scrypt.Key([]byte(passphrase), salt, scryptN, scryptR, scryptP, keyLen)
	if err != nil {
		return err
	}
	var nonce [24]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	var secretKey [32]byte
	copy(secretKey[:], key)

	box := secretbox.Seal(nil, w.Private, &nonce, &secretKey)

	ef := encryptedFile{
		Salt:  base58.Encode(salt),
		Nonce: base58.Encode(nonce[:]),
		Box:   base58.Encode(box),
	}
	b, err := json.MarshalIndent(ef, "", "  ")
	if err != nil {
		return err
	}
	p, err := filePath()
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o600)
}

var ErrWrongPassphrase = errors.New("wrong passphrase")

// Load decrypts ~/.t2b/wallet.enc with the given passphrase.
func Load(passphrase string) (*Wallet, error) {
	p, err := filePath()
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var ef encryptedFile
	if err := json.Unmarshal(b, &ef); err != nil {
		return nil, err
	}
	salt, err := base58.Decode(ef.Salt)
	if err != nil {
		return nil, err
	}
	nonceB, err := base58.Decode(ef.Nonce)
	if err != nil {
		return nil, err
	}
	box, err := base58.Decode(ef.Box)
	if err != nil {
		return nil, err
	}
	key, err := scrypt.Key([]byte(passphrase), salt, scryptN, scryptR, scryptP, keyLen)
	if err != nil {
		return nil, err
	}
	var nonce [24]byte
	copy(nonce[:], nonceB)
	var secretKey [32]byte
	copy(secretKey[:], key)

	priv, ok := secretbox.Open(nil, box, &nonce, &secretKey)
	if !ok {
		return nil, ErrWrongPassphrase
	}
	pk := ed25519.PrivateKey(priv)
	w := &Wallet{Private: pk, Public: pk.Public().(ed25519.PublicKey)}
	// constant-time sanity check the derived pubkey matches the seed tail
	if subtle.ConstantTimeCompare(pk[32:], w.Public) != 1 {
		return nil, ErrWrongPassphrase
	}
	return w, nil
}

// Exists reports whether an encrypted wallet file is already on disk.
func Exists() bool {
	p, err := filePath()
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return err == nil
}
