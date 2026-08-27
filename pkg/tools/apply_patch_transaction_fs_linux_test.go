//go:build linux

package tools

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestApplyPatchTransactionFSStageWitnessPublish(t *testing.T) {
	directory := t.TempDir()
	anchor, err := openApplyPatchTxnAnchor(directory)
	if err != nil {
		t.Fatalf("openApplyPatchTxnAnchor() error = %v", err)
	}
	t.Cleanup(func() { _ = anchor.Close() })

	stage, stageIdentity, err := applyPatchTxnCreateRegular(anchor, ".stage", 0o600)
	if err != nil {
		t.Fatalf("applyPatchTxnCreateRegular() error = %v", err)
	}
	writeErr := applyPatchTxnWriteRegular(stage, []byte("candidate\n"), 0o640, true)
	if writeErr != nil {
		_ = stage.Close()
		t.Fatalf("applyPatchTxnWriteRegular() error = %v", writeErr)
	}
	closeErr := stage.Close()
	if closeErr != nil {
		t.Fatalf("stage.Close() error = %v", closeErr)
	}
	linkErr := applyPatchTxnLinkWitness(
		anchor,
		".stage",
		stageIdentity,
		2,
		anchor,
		".witness",
		applyPatchTransactionTestPrivateName("remove", "fs-witness"),
	)
	if linkErr != nil {
		t.Fatalf("applyPatchTxnLinkWitness() error = %v", linkErr)
	}
	linkedStage, err := applyPatchTxnInspectAt(anchor, ".stage")
	if err != nil || linkedStage.Links != 2 {
		t.Fatalf("linked stage state = %+v, %v; want two links", linkedStage, err)
	}
	renameErr := applyPatchTxnRenameNoReplace(anchor, ".stage", anchor, "target.txt")
	if renameErr != nil {
		t.Fatalf("applyPatchTxnRenameNoReplace() error = %v", renameErr)
	}
	syncErr := applyPatchTxnSyncDirectory(anchor)
	if syncErr != nil {
		t.Fatalf("applyPatchTxnSyncDirectory() error = %v", syncErr)
	}

	targetIdentity, targetMode, err := applyPatchTxnIdentityAt(anchor, "target.txt")
	if err != nil {
		t.Fatalf("target identity error = %v", err)
	}
	witnessIdentity, _, err := applyPatchTxnIdentityAt(anchor, ".witness")
	if err != nil {
		t.Fatalf("witness identity error = %v", err)
	}
	if !targetIdentity.equal(stageIdentity) || !witnessIdentity.equal(stageIdentity) {
		t.Fatalf(
			"stage/target/witness identities differ: stage=%+v target=%+v witness=%+v",
			stageIdentity,
			targetIdentity,
			witnessIdentity,
		)
	}
	if targetMode.Perm() != 0o640 {
		t.Fatalf("target mode = %#o, want 0640", targetMode.Perm())
	}
	removeErr := applyPatchTxnRemoveExact(
		anchor,
		".witness",
		applyPatchTransactionTestPrivateName("remove", "fs-witness"),
		witnessIdentity,
		false,
	)
	if removeErr != nil {
		t.Fatalf("remove witness error = %v", removeErr)
	}
	unwitnessedTarget, err := applyPatchTxnInspectAt(anchor, "target.txt")
	if err != nil || unwitnessedTarget.Links != 1 {
		t.Fatalf("unwitnessed target state = %+v, %v; want one link", unwitnessedTarget, err)
	}
	data, err := os.ReadFile(filepath.Join(directory, "target.txt"))
	if err != nil || string(data) != "candidate\n" {
		t.Fatalf("published target = %q, %v", data, err)
	}
}

func TestApplyPatchTransactionFSWitnessRejectsWrongIdentity(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "source"), []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	anchor, err := openApplyPatchTxnAnchor(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = anchor.Close() })
	identity, _, identityErr := applyPatchTxnIdentityAt(anchor, "source")
	if identityErr != nil {
		t.Fatal(identityErr)
	}
	wrong := identity
	wrong.File++
	linkErr := applyPatchTxnLinkWitness(
		anchor,
		"source",
		wrong,
		2,
		anchor,
		".witness",
		applyPatchTransactionTestPrivateName("remove", "fs-wrong-witness"),
	)
	if linkErr == nil {
		t.Fatal("witness accepted the wrong expected identity")
	}
	_, witnessErr := os.Lstat(filepath.Join(directory, ".witness"))
	if !errors.Is(witnessErr, os.ErrNotExist) {
		t.Fatalf("rejected witness remained: %v", witnessErr)
	}
	state, err := applyPatchTxnInspectAt(anchor, "source")
	if err != nil || state.Links != 1 || !state.Identity.equal(identity) {
		t.Fatalf("source after rejected witness = %+v, %v", state, err)
	}
}

func TestApplyPatchTransactionFSRemoveExactRevalidatesAfterHook(t *testing.T) {
	directory := t.TempDir()
	sourcePath := filepath.Join(directory, "source")
	if err := os.WriteFile(sourcePath, []byte("owned"), 0o600); err != nil {
		t.Fatal(err)
	}
	anchor, anchorErr := openApplyPatchTxnAnchor(directory)
	if anchorErr != nil {
		t.Fatal(anchorErr)
	}
	t.Cleanup(func() { _ = anchor.Close() })
	identity, _, identityErr := applyPatchTxnIdentityAt(anchor, "source")
	if identityErr != nil {
		t.Fatal(identityErr)
	}
	removalName := applyPatchTransactionTestPrivateName("remove", "hook-canary")
	removalPath := filepath.Join(directory, removalName)
	heldPath := filepath.Join(directory, "held-owned")
	removeErr := applyPatchTxnRemoveExact(
		anchor,
		"source",
		removalName,
		identity,
		false,
		func() error {
			if err := os.Rename(removalPath, heldPath); err != nil {
				return err
			}
			return os.WriteFile(removalPath, []byte("alien"), 0o600)
		},
	)
	if removeErr == nil {
		t.Fatal("remove exact accepted a post-hook quarantine replacement")
	}
	data, readErr := os.ReadFile(removalPath)
	if readErr != nil || string(data) != "alien" {
		t.Fatalf("alien removal canary changed: %q, %v", data, readErr)
	}
	held, heldReadErr := os.ReadFile(heldPath)
	if heldReadErr != nil || string(held) != "owned" {
		t.Fatalf("owned quarantined file changed: %q, %v", held, heldReadErr)
	}
}

func TestApplyPatchTransactionFSRemoveExactRejectsPostHookHardLink(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "source"), []byte("owned"), 0o600); err != nil {
		t.Fatal(err)
	}
	anchor, anchorErr := openApplyPatchTxnAnchor(directory)
	if anchorErr != nil {
		t.Fatal(anchorErr)
	}
	t.Cleanup(func() { _ = anchor.Close() })
	identity, _, identityErr := applyPatchTxnIdentityAt(anchor, "source")
	if identityErr != nil {
		t.Fatal(identityErr)
	}
	removalName := applyPatchTransactionTestPrivateName("remove", "hook-link")
	removalPath := filepath.Join(directory, removalName)
	aliasPath := filepath.Join(directory, "alien-alias")
	removeErr := applyPatchTxnRemoveExact(
		anchor,
		"source",
		removalName,
		identity,
		false,
		func() error { return os.Link(removalPath, aliasPath) },
	)
	if removeErr == nil {
		t.Fatal("remove exact accepted a post-hook hard link")
	}
	removalState, inspectErr := applyPatchTxnInspectAt(anchor, removalName)
	if inspectErr != nil || !removalState.Identity.equal(identity) || removalState.Links != 2 {
		t.Fatalf("owned removal after link race = %+v, %v", removalState, inspectErr)
	}
	aliasState, inspectErr := applyPatchTxnInspectAt(anchor, "alien-alias")
	if inspectErr != nil || !aliasState.Identity.equal(identity) || aliasState.Links != 2 {
		t.Fatalf("alien alias after link race = %+v, %v", aliasState, inspectErr)
	}
}

func TestApplyPatchTransactionFSNoReplaceAndQuarantine(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "source"), []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "occupied"), []byte("alien"), 0o600); err != nil {
		t.Fatal(err)
	}
	anchor, err := openApplyPatchTxnAnchor(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = anchor.Close() })
	sourceIdentity, _, err := applyPatchTxnIdentityAt(anchor, "source")
	if err != nil {
		t.Fatal(err)
	}
	renameErr := applyPatchTxnRenameNoReplace(anchor, "source", anchor, "occupied")
	if renameErr == nil {
		t.Fatal("no-replace rename over occupied destination succeeded")
	}
	occupiedData, occupiedErr := os.ReadFile(filepath.Join(directory, "occupied"))
	if occupiedErr != nil || string(occupiedData) != "alien" {
		t.Fatalf("occupied destination changed: %q, %v", occupiedData, occupiedErr)
	}
	quarantineErr := applyPatchTxnQuarantineExact(anchor, "source", ".quarantine", sourceIdentity)
	if quarantineErr != nil {
		t.Fatalf("quarantine exact error = %v", quarantineErr)
	}
	_, sourceErr := os.Lstat(filepath.Join(directory, "source"))
	if !errors.Is(sourceErr, os.ErrNotExist) {
		t.Fatalf("source still present after quarantine: %v", sourceErr)
	}
	quarantineIdentity, _, err := applyPatchTxnIdentityAt(anchor, ".quarantine")
	if err != nil || !quarantineIdentity.equal(sourceIdentity) {
		t.Fatalf("quarantine identity = %+v, %v; want %+v", quarantineIdentity, err, sourceIdentity)
	}
}

func TestApplyPatchTransactionFSDirectoryForestPrimitives(t *testing.T) {
	root := t.TempDir()
	anchor, err := openApplyPatchTxnAnchor(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = anchor.Close() })
	directoryIdentity, err := applyPatchTxnMkdir(anchor, ".forest", 0o755)
	if err != nil {
		t.Fatal(err)
	}
	forest, err := applyPatchTxnOpenChildDirectory(anchor, ".forest")
	if err != nil {
		t.Fatal(err)
	}
	file, fileIdentity, err := applyPatchTxnCreateRegular(forest, "member", 0o600)
	if err != nil {
		_ = forest.Close()
		t.Fatal(err)
	}
	if writeErr := applyPatchTxnWriteRegular(file, []byte("member\n"), 0, false); writeErr != nil {
		_ = file.Close()
		_ = forest.Close()
		t.Fatal(writeErr)
	}
	if closeErr := file.Close(); closeErr != nil {
		_ = forest.Close()
		t.Fatal(closeErr)
	}
	data, mode, readIdentity, err := applyPatchTxnReadRegular(forest, "member", 7)
	if err != nil || string(data) != "member\n" || mode.Perm() != 0o600 ||
		!readIdentity.equal(fileIdentity) {
		_ = forest.Close()
		t.Fatalf("forest member = %q mode=%#o identity=%+v, %v", data, mode, readIdentity, err)
	}
	if _, _, _, err := applyPatchTxnReadRegular(forest, "member", 6); err == nil {
		_ = forest.Close()
		t.Fatal("bounded read accepted oversized member")
	}
	if err := applyPatchTxnRemoveExact(
		forest,
		"member",
		applyPatchTransactionTestPrivateName("remove", "fs-forest-member"),
		fileIdentity,
		false,
	); err != nil {
		_ = forest.Close()
		t.Fatal(err)
	}
	if err := forest.Close(); err != nil {
		t.Fatal(err)
	}
	if err := applyPatchTxnRemoveExact(
		anchor,
		".forest",
		applyPatchTransactionTestPrivateName("remove", "fs-forest-root"),
		directoryIdentity,
		true,
	); err != nil {
		t.Fatal(err)
	}
}

func TestApplyPatchTransactionFSRejectsSymlinkAnchorAndIdentityMismatch(t *testing.T) {
	root := t.TempDir()
	realDirectory := filepath.Join(root, "real")
	if err := os.Mkdir(realDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	if _, err := openApplyPatchTxnAnchor(filepath.Join(root, "alias")); err == nil {
		t.Fatal("symlinked anchor was accepted")
	}
	if err := os.WriteFile(filepath.Join(realDirectory, "canary"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	anchor, err := openApplyPatchTxnAnchor(realDirectory)
	if err != nil {
		t.Fatal(err)
	}
	identity, _, err := applyPatchTxnIdentityAt(anchor, "canary")
	if err != nil {
		t.Fatal(err)
	}
	wrong := identity
	wrong.File++
	if err := applyPatchTxnRemoveExact(
		anchor,
		"canary",
		applyPatchTransactionTestPrivateName("remove", "fs-canary"),
		wrong,
		false,
	); err == nil {
		t.Fatal("identity-mismatched removal succeeded")
	}
	if data, err := os.ReadFile(filepath.Join(realDirectory, "canary")); err != nil || string(data) != "keep" {
		t.Fatalf("identity-mismatch removed canary: %q, %v", data, err)
	}
	if err := anchor.Close(); err != nil {
		t.Fatalf("anchor.Close() error = %v", err)
	}
	if err := anchor.Close(); err != nil {
		t.Fatalf("second anchor.Close() error = %v", err)
	}
	if _, _, err := applyPatchTxnIdentityAt(anchor, "canary"); err == nil {
		t.Fatal("closed anchor remained usable")
	}
}

func TestApplyPatchTransactionFSRemovalQuarantineResumesAfterInterruption(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "owned")
	if err := os.WriteFile(path, []byte("owned\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	anchor, err := openApplyPatchTxnAnchor(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer anchor.Close()
	identity, _, err := applyPatchTxnIdentityAt(anchor, "owned")
	if err != nil {
		t.Fatal(err)
	}
	removal := applyPatchTransactionTestPrivateName("remove", "interrupted-removal")
	injected := errors.New("interrupted after removal quarantine")
	err = applyPatchTxnRemoveExact(
		anchor,
		"owned",
		removal,
		identity,
		false,
		func() error { return injected },
	)
	if !errors.Is(err, injected) {
		t.Fatalf("interrupted removal error = %v", err)
	}
	_, originalErr := os.Lstat(path)
	if !errors.Is(originalErr, os.ErrNotExist) {
		t.Fatalf("original removal name remained: %v", originalErr)
	}
	removalIdentity, _, err := applyPatchTxnIdentityAt(anchor, removal)
	if err != nil || !removalIdentity.equal(identity) {
		t.Fatalf("removal quarantine = %+v, %v; want %+v", removalIdentity, err, identity)
	}
	if err := applyPatchTxnRemoveExact(
		anchor,
		"owned",
		removal,
		identity,
		false,
	); err != nil {
		t.Fatalf("resumed exact removal error = %v", err)
	}
	if _, _, err := applyPatchTxnIdentityAt(anchor, removal); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removal quarantine remained after resume: %v", err)
	}
}
