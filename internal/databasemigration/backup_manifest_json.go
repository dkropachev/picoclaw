package databasemigration

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

const backupManifestJSONTokenLimit = backupMaxEntries*20 + backupMaxFiles*24

type backupManifestJSONBudget struct {
	tokens          int
	legacyRoots     int
	legacyRootKinds int
}

func preflightBackupManifestJSON(payload []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	budget := &backupManifestJSONBudget{tokens: backupManifestJSONTokenLimit}
	if err := backupManifestJSONDelimiter(decoder, budget, '{'); err != nil {
		return err
	}
	seen := make(map[string]struct{}, 6)
	for decoder.More() {
		name, err := backupManifestJSONField(decoder, budget, seen)
		if err != nil {
			return err
		}
		switch name {
		case "version", "created_at", "capture_mode":
			err = backupManifestJSONScalar(decoder, budget)
		case "stores":
			err = backupManifestJSONArray(decoder, budget, backupMaxEntries, func() error {
				return backupManifestJSONStore(decoder, budget)
			})
		case "files":
			err = backupManifestJSONArray(decoder, budget, backupMaxFiles, func() error {
				return backupManifestJSONFile(decoder, budget)
			})
		case "catalog_generations":
			err = backupManifestJSONArray(decoder, budget, backupMaxEntries, func() error {
				return backupManifestJSONString(decoder, budget)
			})
		default:
			return errors.New("database backup manifest JSON field is unknown")
		}
		if err != nil {
			return err
		}
	}
	if err := backupManifestJSONDelimiter(decoder, budget, '}'); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("database backup manifest JSON has trailing data")
	}
	return nil
}

func backupManifestJSONStore(decoder *json.Decoder, budget *backupManifestJSONBudget) error {
	if err := backupManifestJSONDelimiter(decoder, budget, '{'); err != nil {
		return err
	}
	seen := make(map[string]struct{}, 5)
	for decoder.More() {
		name, err := backupManifestJSONField(decoder, budget, seen)
		if err != nil {
			return err
		}
		switch name {
		case "store_id", "path", "exists":
			err = backupManifestJSONScalar(decoder, budget)
		case "legacy_roots":
			err = backupManifestJSONStringArray(
				decoder, budget, &budget.legacyRoots, backupMaxLegacyRoots,
			)
		case "legacy_root_kinds":
			err = backupManifestJSONStringArray(
				decoder, budget, &budget.legacyRootKinds, backupMaxLegacyRoots,
			)
		default:
			return errors.New("database backup store JSON field is unknown")
		}
		if err != nil {
			return err
		}
	}
	return backupManifestJSONDelimiter(decoder, budget, '}')
}

func backupManifestJSONFile(decoder *json.Decoder, budget *backupManifestJSONBudget) error {
	if err := backupManifestJSONDelimiter(decoder, budget, '{'); err != nil {
		return err
	}
	seen := make(map[string]struct{}, 10)
	for decoder.More() {
		name, err := backupManifestJSONField(decoder, budget, seen)
		if err != nil {
			return err
		}
		switch name {
		case "store_id", "role", "legacy_root", "source", "source_identity",
			"backup", "sha256", "size", "mode", "source_mode":
			err = backupManifestJSONScalar(decoder, budget)
		default:
			return errors.New("database backup file JSON field is unknown")
		}
		if err != nil {
			return err
		}
	}
	return backupManifestJSONDelimiter(decoder, budget, '}')
}

func backupManifestJSONStringArray(
	decoder *json.Decoder,
	budget *backupManifestJSONBudget,
	total *int,
	limit int,
) error {
	return backupManifestJSONArray(decoder, budget, limit, func() error {
		*total++
		if *total > limit {
			return errors.New("database backup manifest JSON legacy-root limit exceeded")
		}
		return backupManifestJSONString(decoder, budget)
	})
}

func backupManifestJSONArray(
	decoder *json.Decoder,
	budget *backupManifestJSONBudget,
	limit int,
	value func() error,
) error {
	if err := backupManifestJSONDelimiter(decoder, budget, '['); err != nil {
		return err
	}
	count := 0
	for decoder.More() {
		count++
		if count > limit {
			return errors.New("database backup manifest JSON array limit exceeded")
		}
		if err := value(); err != nil {
			return err
		}
	}
	return backupManifestJSONDelimiter(decoder, budget, ']')
}

func backupManifestJSONField(
	decoder *json.Decoder,
	budget *backupManifestJSONBudget,
	seen map[string]struct{},
) (string, error) {
	token, err := backupManifestJSONToken(decoder, budget)
	name, ok := token.(string)
	if err != nil || !ok {
		return "", errors.Join(errors.New("database backup manifest JSON field is invalid"), err)
	}
	if len(name) > 32 {
		return "", errors.New("database backup manifest JSON field is too long")
	}
	if _, duplicate := seen[name]; duplicate {
		return "", errors.New("database backup manifest JSON field is duplicated")
	}
	seen[name] = struct{}{}
	return name, nil
}

func backupManifestJSONString(decoder *json.Decoder, budget *backupManifestJSONBudget) error {
	token, err := backupManifestJSONToken(decoder, budget)
	if _, ok := token.(string); err != nil || !ok {
		return errors.Join(errors.New("database backup manifest JSON string is invalid"), err)
	}
	return nil
}

func backupManifestJSONScalar(decoder *json.Decoder, budget *backupManifestJSONBudget) error {
	token, err := backupManifestJSONToken(decoder, budget)
	if _, nested := token.(json.Delim); err != nil || nested {
		return errors.Join(errors.New("database backup manifest JSON scalar is invalid"), err)
	}
	return nil
}

func backupManifestJSONDelimiter(
	decoder *json.Decoder,
	budget *backupManifestJSONBudget,
	want json.Delim,
) error {
	token, err := backupManifestJSONToken(decoder, budget)
	delimiter, ok := token.(json.Delim)
	if err != nil || !ok || delimiter != want {
		return errors.Join(errors.New("database backup manifest JSON structure is invalid"), err)
	}
	return nil
}

func backupManifestJSONToken(
	decoder *json.Decoder,
	budget *backupManifestJSONBudget,
) (json.Token, error) {
	if decoder == nil || budget == nil || budget.tokens <= 0 {
		return nil, errors.New("database backup manifest JSON token limit exceeded")
	}
	token, err := decoder.Token()
	if err == nil {
		budget.tokens--
	}
	return token, err
}
