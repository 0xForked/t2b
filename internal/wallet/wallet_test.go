package wallet

import "testing"

func TestGenerateSaveLoadRoundtrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	w, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Save("correct horse"); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.PublicBase58() != w.PublicBase58() {
		t.Fatalf("pubkey mismatch: got %s want %s", loaded.PublicBase58(), w.PublicBase58())
	}

	if _, err := Load("wrong passphrase"); err != ErrWrongPassphrase {
		t.Fatalf("expected ErrWrongPassphrase, got %v", err)
	}
}

func TestImportBase58Roundtrip(t *testing.T) {
	w, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	imported, err := ImportBase58(w.PrivateBase58())
	if err != nil {
		t.Fatal(err)
	}
	if imported.PublicBase58() != w.PublicBase58() {
		t.Fatal("imported key does not match original")
	}
}
