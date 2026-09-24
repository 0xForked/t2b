package journal

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOpenWriteAppendsJSONLines(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	j, err := Open()
	if err != nil {
		t.Fatal(err)
	}
	j.Write(Entry{At: time.Now(), Kind: "BUY", Symbol: "AAA", Mint: "mint1"})
	j.Write(Entry{At: time.Now(), Kind: "SELL_TP", Symbol: "AAA", Mint: "mint1", PnlSOL: 0.05})
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}

	home, _ := os.UserHomeDir()
	f, err := os.Open(filepath.Join(home, ".t2b", "logs", "trades.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if len(lines) != 2 {
		t.Fatalf("expected 2 journal lines, got %d", len(lines))
	}
	var e Entry
	if err := json.Unmarshal([]byte(lines[1]), &e); err != nil {
		t.Fatal(err)
	}
	if e.Kind != "SELL_TP" || e.PnlSOL != 0.05 {
		t.Fatalf("unexpected decoded entry: %+v", e)
	}
}

func TestNilJournalIsSafeNoOp(t *testing.T) {
	var j *Journal
	j.Write(Entry{Kind: "BUY"}) // must not panic
	if err := j.Close(); err != nil {
		t.Fatalf("expected nil error closing nil journal, got %v", err)
	}
}
