package appliance

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const (
	releaseTransactionPrepared  = "prepared"
	releaseTransactionCommitted = "committed"
)

type releaseTransaction struct {
	SchemaVersion  int    `json:"schemaVersion"`
	Phase          string `json:"phase"`
	FinalPath      string `json:"finalPath"`
	BackupPath     string `json:"backupPath,omitempty"`
	PreviousTarget string `json:"previousTarget,omitempty"`
	HadPrevious    bool   `json:"hadPrevious"`
	RuntimeSHA256  string `json:"runtimeSha256,omitempty"`
	TransactionID  string `json:"transactionId,omitempty"`
}

func transactionForActivation(activation *ReleaseActivation, phase string) releaseTransaction {
	return releaseTransaction{
		SchemaVersion:  2,
		Phase:          phase,
		FinalPath:      activation.finalPath,
		BackupPath:     activation.backupPath,
		PreviousTarget: activation.previousTarget,
		HadPrevious:    activation.hadPrevious,
		RuntimeSHA256:  activation.runtimeSHA256,
		TransactionID:  activation.transactionID,
	}
}

func writeReleaseTransaction(activation *ReleaseActivation, phase string) error {
	if activation == nil || activation.paths.UpdateStatePath == "" {
		return errors.New("runtime update transaction path is unavailable")
	}
	document := transactionForActivation(activation, phase)
	if err := validateReleaseTransaction(activation.paths, document); err != nil {
		return err
	}
	data, err := json.Marshal(document)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	directory := filepath.Dir(activation.paths.UpdateStatePath)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	if phase == releaseTransactionPrepared {
		file, err := os.OpenFile(activation.paths.UpdateStatePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			if errors.Is(err, os.ErrExist) {
				return errors.New("another runtime update transaction requires recovery")
			}
			return err
		}
		writeErr := func() error {
			if _, err := file.Write(data); err != nil {
				return err
			}
			return file.Sync()
		}()
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			// The incomplete O_EXCL file remains as a fail-closed recovery marker;
			// never remove it and permit a second transaction to overwrite the
			// uncertain first write.
			return errors.Join(writeErr, closeErr)
		}
		return syncDirectory(directory)
	}
	existing, found, err := readReleaseTransaction(activation.paths)
	if err != nil {
		return err
	}
	prepared := transactionForActivation(activation, releaseTransactionPrepared)
	if !found || existing != prepared {
		return errors.New("runtime update commit does not match the prepared transaction")
	}
	temporary, err := os.CreateTemp(directory, ".update-transaction-*")
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
	if err := os.Rename(temporaryPath, activation.paths.UpdateStatePath); err != nil {
		return err
	}
	return syncDirectory(directory)
}

func clearReleaseTransaction(paths Paths) error {
	err := os.Remove(paths.UpdateStatePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(paths.UpdateStatePath))
}

func readReleaseTransaction(paths Paths) (releaseTransaction, bool, error) {
	data, err := os.ReadFile(paths.UpdateStatePath)
	if errors.Is(err, os.ErrNotExist) {
		return releaseTransaction{}, false, nil
	}
	if err != nil {
		return releaseTransaction{}, false, err
	}
	var transaction releaseTransaction
	if err := DecodeStrict(data, &transaction); err != nil {
		return releaseTransaction{}, false, errors.New("runtime update transaction is invalid")
	}
	if err := validateReleaseTransaction(paths, transaction); err != nil {
		return releaseTransaction{}, false, err
	}
	return transaction, true, nil
}

func validateReleaseTransaction(paths Paths, transaction releaseTransaction) error {
	if (transaction.SchemaVersion != 1 && transaction.SchemaVersion != 2) || (transaction.Phase != releaseTransactionPrepared && transaction.Phase != releaseTransactionCommitted) {
		return errors.New("runtime update transaction schema is invalid")
	}
	if transaction.SchemaVersion == 2 {
		metadata := releaseMetadata{SchemaVersion: 1, Version: filepath.Base(transaction.FinalPath), RuntimeSHA256: transaction.RuntimeSHA256, TransactionID: transaction.TransactionID}
		if err := metadata.Validate(); err != nil {
			return errors.New("runtime update transaction identity is invalid")
		}
	} else if transaction.RuntimeSHA256 != "" || transaction.TransactionID != "" {
		return errors.New("legacy runtime update transaction contains unsupported identity")
	}
	if !isWithinRuntimeRoot(transaction.FinalPath, paths.ReleaseRoot) {
		return errors.New("runtime update target escapes the release root")
	}
	if transaction.BackupPath != "" && !isWithinRuntimeRoot(transaction.BackupPath, paths.ReleaseRoot) {
		return errors.New("runtime update backup escapes the release root")
	}
	if transaction.HadPrevious {
		previous := transaction.PreviousTarget
		if !filepath.IsAbs(previous) {
			previous = filepath.Join(filepath.Dir(paths.CurrentLink), previous)
		}
		if !isWithinRuntimeRoot(filepath.Clean(previous), paths.ReleaseRoot) {
			return errors.New("runtime update previous target escapes the release root")
		}
	} else if transaction.PreviousTarget != "" {
		return errors.New("runtime update previous target is inconsistent")
	}
	return nil
}

// recoverInterruptedRelease is invoked before the supervisor exposes its
// control socket.  A prepared transaction never received a durable commit, so
// it is conservatively rolled back.  A committed marker means only cleanup was
// interrupted and must never revert the healthy release.
func recoverInterruptedRelease(paths Paths) error {
	return recoverInterruptedReleaseWithOperations(paths, releaseOperations{})
}

func recoverInterruptedReleaseWithOperations(paths Paths, operations releaseOperations) error {
	operations = operations.withDefaults()
	transaction, found, err := readReleaseTransaction(paths)
	if err != nil || !found {
		return err
	}
	if transaction.Phase == releaseTransactionCommitted {
		if transaction.BackupPath != "" {
			if err := operations.removeRuntimeTree(transaction.BackupPath, paths.ReleaseRoot); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err := operations.syncDirectory(paths.ReleaseRoot); err != nil {
				return err
			}
		}
		return clearReleaseTransaction(paths)
	}
	if err := operations.unmountWrapperState(filepath.Join(transaction.FinalPath, "rootfs", "data")); err != nil {
		return fmt.Errorf("unmount wrapper state before interrupted runtime recovery: %w", err)
	}

	if transaction.BackupPath != "" {
		if _, backupErr := os.Lstat(transaction.BackupPath); backupErr == nil {
			if _, finalErr := os.Lstat(transaction.FinalPath); finalErr == nil {
				if err := operations.removeRuntimeTree(transaction.FinalPath, paths.ReleaseRoot); err != nil && !errors.Is(err, os.ErrNotExist) {
					return fmt.Errorf("remove interrupted runtime: %w", err)
				}
			} else if !errors.Is(finalErr, os.ErrNotExist) {
				return fmt.Errorf("inspect interrupted runtime: %w", finalErr)
			}
			if err := operations.rename(transaction.BackupPath, transaction.FinalPath); err != nil {
				return fmt.Errorf("restore interrupted runtime backup: %w", err)
			}
			if err := operations.syncDirectory(paths.ReleaseRoot); err != nil {
				return fmt.Errorf("sync recovered runtime directory: %w", err)
			}
		} else if !errors.Is(backupErr, os.ErrNotExist) {
			return backupErr
		} else {
			// A missing backup is safe only after a prior rollback already moved
			// it back to FinalPath and then crashed before restoring the DB or
			// clearing the marker. If the transaction's target identity is still
			// installed, clearing here would combine the new runtime with old DB
			// state and permanently discard the only recovery evidence.
			if transaction.SchemaVersion != 2 {
				return errors.New("runtime update backup is missing and legacy transaction identity cannot prove rollback")
			}
			installed, metadataErr := readReleaseMetadataAt(transaction.FinalPath)
			if metadataErr != nil {
				return fmt.Errorf("runtime update backup is missing and restored runtime identity is unavailable: %w", metadataErr)
			}
			if installed.RuntimeSHA256 == transaction.RuntimeSHA256 && installed.TransactionID == transaction.TransactionID {
				return errors.New("runtime update backup is missing while the target runtime identity remains installed")
			}
		}
	}
	if transaction.HadPrevious {
		previous := transaction.PreviousTarget
		if !filepath.IsAbs(previous) {
			previous = filepath.Join(filepath.Dir(paths.CurrentLink), previous)
		}
		if err := switchCurrentLink(paths.CurrentLink, previous); err != nil {
			return err
		}
	} else if info, err := os.Lstat(paths.CurrentLink); err == nil && info.Mode()&os.ModeSymlink != 0 {
		if err := os.Remove(paths.CurrentLink); err != nil {
			return err
		}
		if err := syncDirectory(filepath.Dir(paths.CurrentLink)); err != nil {
			return err
		}
	}
	restoreDatabase := restoreJobsDBBackup
	if transaction.SchemaVersion == 2 {
		restoreDatabase = restoreJobsDBBackupRequired
	}
	if err := restoreDatabase(paths.DataDir); err != nil {
		return fmt.Errorf("restore jobs database after interrupted update: %w", err)
	}
	return clearReleaseTransaction(paths)
}
