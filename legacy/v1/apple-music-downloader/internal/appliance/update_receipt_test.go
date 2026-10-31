package appliance

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func receiptFixture() updateReceipt {
	return updateReceipt{
		SchemaVersion: updateReceiptSchemaVersion,
		TransactionID: strings.Repeat("3", 64),
		Outcome:       updateOutcomeRolledBack,
		Source: releaseMetadata{
			SchemaVersion: 1,
			Version:       "1.2.0",
			RuntimeSHA256: strings.Repeat("1", 64),
			TransactionID: strings.Repeat("2", 64),
		},
		Target: releaseMetadata{
			SchemaVersion: 1,
			Version:       "1.2.0",
			RuntimeSHA256: strings.Repeat("4", 64),
			TransactionID: strings.Repeat("3", 64),
		},
	}
}

func TestUpdateReceiptAtomicRoundTripAndStrictSchema(t *testing.T) {
	paths := testPaths(t.TempDir())
	want := receiptFixture()
	if err := writeUpdateReceipt(paths, want); err != nil {
		t.Fatal(err)
	}
	got, found, err := readUpdateReceipt(paths)
	if err != nil || !found || got != want {
		t.Fatalf("receipt=%#v found=%v err=%v", got, found, err)
	}
	if info, err := os.Stat(paths.UpdateReceiptPath); err != nil {
		t.Fatal(err)
	} else if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("receipt mode = %o", info.Mode().Perm())
	}
	invalid := []byte(`{"schemaVersion":1,"transactionId":"` + strings.Repeat("3", 64) +
		`","outcome":"rolled_back","source":{},"target":{},"unexpected":true}`)
	if err := os.WriteFile(paths.UpdateReceiptPath, invalid, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readUpdateReceipt(paths); err == nil {
		t.Fatal("receipt with unknown field was accepted")
	}
}

func TestUpdateReceiptRefusesNonRegularDestination(t *testing.T) {
	paths := testPaths(t.TempDir())
	if err := os.MkdirAll(filepath.Dir(paths.UpdateReceiptPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(paths.UpdateReceiptPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeUpdateReceipt(paths, receiptFixture()); err == nil {
		t.Fatal("directory receipt destination was accepted")
	}
}
