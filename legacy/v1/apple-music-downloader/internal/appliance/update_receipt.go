package appliance

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	updateReceiptSchemaVersion = 1
	maxUpdateReceiptBytes      = 16 * 1024
	updateOutcomeCommitted     = "committed"
	updateOutcomeRolledBack    = "rolled_back"
)

// updateReceipt is the durable terminal evidence for one desktop-owned
// runtime update. A rolled-back source release does not contain the failed
// transaction ID, so the Windows host must never infer rollback from version
// or digest stability alone.
type updateReceipt struct {
	SchemaVersion int             `json:"schemaVersion"`
	TransactionID string          `json:"transactionId"`
	Outcome       string          `json:"outcome"`
	Source        releaseMetadata `json:"source"`
	Target        releaseMetadata `json:"target"`
}

func (receipt updateReceipt) Validate() error {
	if receipt.SchemaVersion != updateReceiptSchemaVersion ||
		(receipt.Outcome != updateOutcomeCommitted && receipt.Outcome != updateOutcomeRolledBack) ||
		!sha256Pattern.MatchString(receipt.TransactionID) ||
		receipt.TransactionID != strings.ToLower(receipt.TransactionID) {
		return errors.New("runtime update receipt schema is invalid")
	}
	if err := receipt.Source.Validate(); err != nil {
		return errors.New("runtime update receipt source identity is invalid")
	}
	if err := receipt.Target.Validate(); err != nil || receipt.Target.TransactionID != receipt.TransactionID {
		return errors.New("runtime update receipt target identity is invalid")
	}
	return nil
}

func newUpdateReceipt(request UpdateRequest, source releaseMetadata, outcome string) (updateReceipt, error) {
	receipt := updateReceipt{
		SchemaVersion: updateReceiptSchemaVersion,
		TransactionID: strings.ToLower(strings.TrimSpace(request.TransactionID)),
		Outcome:       outcome,
		Source:        source,
		Target: releaseMetadata{
			SchemaVersion: 1,
			Version:       request.Version,
			RuntimeSHA256: strings.ToLower(request.SHA256),
			TransactionID: strings.ToLower(strings.TrimSpace(request.TransactionID)),
		},
	}
	if err := receipt.Validate(); err != nil {
		return updateReceipt{}, err
	}
	return receipt, nil
}

func writeUpdateReceipt(paths Paths, receipt updateReceipt) error {
	if err := receipt.Validate(); err != nil {
		return err
	}
	if paths.UpdateReceiptPath == "" || !filepath.IsAbs(paths.UpdateReceiptPath) {
		return errors.New("runtime update receipt path is unavailable")
	}
	directory := filepath.Dir(paths.UpdateReceiptPath)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	directoryInfo, err := os.Lstat(directory)
	if err != nil || !directoryInfo.IsDir() || directoryInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("runtime update receipt directory is invalid")
	}
	if runtime.GOOS != "windows" && directoryInfo.Mode().Perm() != 0o700 {
		return errors.New("runtime update receipt directory permissions are invalid")
	}
	if info, err := os.Lstat(paths.UpdateReceiptPath); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("runtime update receipt path is not a regular file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	data, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if len(data) > maxUpdateReceiptBytes {
		return errors.New("runtime update receipt exceeds the size limit")
	}
	temporary, err := os.CreateTemp(directory, ".update-receipt-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, paths.UpdateReceiptPath); err != nil {
		return err
	}
	return syncDirectory(directory)
}

func readUpdateReceipt(paths Paths) (updateReceipt, bool, error) {
	info, err := os.Lstat(paths.UpdateReceiptPath)
	if errors.Is(err, os.ErrNotExist) {
		return updateReceipt{}, false, nil
	}
	if err != nil {
		return updateReceipt{}, false, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return updateReceipt{}, false, errors.New("runtime update receipt is not a regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		return updateReceipt{}, false, errors.New("runtime update receipt permissions are invalid")
	}
	file, err := os.Open(paths.UpdateReceiptPath)
	if err != nil {
		return updateReceipt{}, false, err
	}
	openedInfo, statErr := file.Stat()
	if statErr != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		_ = file.Close()
		return updateReceipt{}, false, errors.New("runtime update receipt identity changed while opening")
	}
	if runtime.GOOS != "windows" && openedInfo.Mode().Perm() != 0o600 {
		_ = file.Close()
		return updateReceipt{}, false, errors.New("runtime update receipt permissions are invalid")
	}
	data, readErr := ReadLimited(file, maxUpdateReceiptBytes)
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return updateReceipt{}, false, errors.Join(readErr, closeErr)
	}
	defer wipe(data)
	var receipt updateReceipt
	if err := DecodeStrict(data, &receipt); err != nil {
		return updateReceipt{}, false, errors.New("runtime update receipt is invalid")
	}
	if err := receipt.Validate(); err != nil {
		return updateReceipt{}, false, err
	}
	return receipt, true, nil
}

func loadUpdateReceiptForCurrentRuntime(paths Paths) (updateReceipt, bool, error) {
	if _, found, err := readReleaseTransaction(paths); err != nil {
		return updateReceipt{}, false, err
	} else if found {
		return updateReceipt{}, false, errors.New("runtime update receipt cannot be trusted while recovery is pending")
	}
	receipt, found, err := readUpdateReceipt(paths)
	if err != nil || !found {
		return receipt, found, err
	}
	current, err := readCurrentReleaseMetadata(paths)
	if err != nil {
		return updateReceipt{}, false, err
	}
	expected := receipt.Target
	if receipt.Outcome == updateOutcomeRolledBack {
		expected = receipt.Source
	}
	if current != expected {
		return updateReceipt{}, false, errors.New("runtime update receipt conflicts with the installed release identity")
	}
	return receipt, true, nil
}

// recoverInterruptedReleaseWithReceipt runs before the control socket is
// exposed. Prepared schema-v2 transactions are rolled back first and then
// receive a durable receipt binding the restored source to the failed target.
// A committed marker already has authoritative target metadata, so recovery
// never reverts it; a receipt is written when its source backup is still
// available.
func recoverInterruptedReleaseWithReceipt(paths Paths) error {
	transaction, found, err := readReleaseTransaction(paths)
	if err != nil || !found {
		return err
	}
	var source releaseMetadata
	if transaction.SchemaVersion == 2 && transaction.BackupPath != "" {
		if candidate, sourceErr := readReleaseMetadataAt(transaction.BackupPath); sourceErr == nil {
			source = candidate
		}
	}
	phase := transaction.Phase
	if err := recoverInterruptedRelease(paths); err != nil {
		return err
	}
	if transaction.SchemaVersion != 2 {
		return nil
	}
	target := releaseMetadata{
		SchemaVersion: 1,
		Version:       filepath.Base(transaction.FinalPath),
		RuntimeSHA256: transaction.RuntimeSHA256,
		TransactionID: transaction.TransactionID,
	}
	if err := target.Validate(); err != nil {
		return fmt.Errorf("recover runtime update target identity: %w", err)
	}
	current, err := readCurrentReleaseMetadata(paths)
	if err != nil {
		return fmt.Errorf("read recovered runtime identity: %w", err)
	}
	outcome := updateOutcomeCommitted
	if phase == releaseTransactionPrepared {
		outcome = updateOutcomeRolledBack
		source = current
	}
	if source.Validate() != nil {
		// The committed target identity is independently authoritative. A
		// crash after deleting its old backup can make the source unavailable,
		// in which case no incomplete receipt is published.
		return nil
	}
	receipt := updateReceipt{
		SchemaVersion: updateReceiptSchemaVersion,
		TransactionID: target.TransactionID,
		Outcome:       outcome,
		Source:        source,
		Target:        target,
	}
	if err := receipt.Validate(); err != nil {
		return err
	}
	if outcome == updateOutcomeCommitted && current != target {
		return errors.New("committed runtime recovery identity does not match its transaction")
	}
	if outcome == updateOutcomeRolledBack && current != source {
		return errors.New("rolled-back runtime recovery identity does not match its transaction")
	}
	return writeUpdateReceipt(paths, receipt)
}
