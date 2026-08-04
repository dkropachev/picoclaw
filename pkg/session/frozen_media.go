package session

import (
	"context"

	"github.com/sipeed/picoclaw/pkg/media"
	"github.com/sipeed/picoclaw/pkg/providers"
)

type sessionMediaSlot struct {
	locator               *string
	contentType           *string
	filename              *string
	metadataAuthoritative bool
}

// FreezeSessionSnapshotMedia graph-detaches a coherent session snapshot and
// replaces every structured media locator with an immutable frozen reference.
// The returned set contains all referenced bytes and is safe to serialize with
// the returned snapshot. Capture is all-or-nothing and never fetches a remote
// or filesystem locator.
func FreezeSessionSnapshotMedia(
	ctx context.Context,
	snapshot SessionSnapshot,
	reader media.SnapshotReader,
) (SessionSnapshot, media.FrozenSet, error) {
	occurrences, err := countSessionSnapshotMedia(ctx, snapshot.History)
	if err != nil {
		return SessionSnapshot{}, media.FrozenSet{}, err
	}
	frozen := cloneSnapshotForMedia(snapshot)
	slots := sessionSnapshotMediaSlots(frozen.History, occurrences)
	inputs := make([]media.FreezeInput, len(slots))
	for index, slot := range slots {
		inputs[index] = media.FreezeInput{Locator: *slot.locator}
		if slot.contentType != nil {
			inputs[index].ContentType = *slot.contentType
		}
		if slot.filename != nil {
			inputs[index].Filename = *slot.filename
		}
	}

	references, set, err := media.FreezeInputs(ctx, inputs, reader)
	if err != nil {
		return SessionSnapshot{}, media.FrozenSet{}, err
	}
	applyFrozenSessionMedia(slots, references)
	return frozen, set.Clone(), nil
}

// MaterializeSessionSnapshotMedia validates a complete frozen set and returns
// a graph-detached snapshot whose frozen references are canonical data URIs.
// It requires an exact one-to-one set: injected, missing, or unused assets fail.
func MaterializeSessionSnapshotMedia(
	ctx context.Context,
	snapshot SessionSnapshot,
	set media.FrozenSet,
) (SessionSnapshot, error) {
	occurrences, err := countSessionSnapshotMedia(ctx, snapshot.History)
	if err != nil {
		return SessionSnapshot{}, err
	}
	if validateErr := set.Validate(); validateErr != nil {
		return SessionSnapshot{}, validateErr
	}
	materialized := cloneSnapshotForMedia(snapshot)
	slots := sessionSnapshotMediaSlots(materialized.History, occurrences)
	refs := make([]string, len(slots))
	for index, slot := range slots {
		refs[index] = *slot.locator
	}
	values, err := set.Materialize(ctx, refs)
	if err != nil {
		return SessionSnapshot{}, err
	}
	if err := validateMaterializedSessionMetadata(slots, values); err != nil {
		return SessionSnapshot{}, err
	}
	applyMaterializedSessionMedia(slots, values)
	return materialized, nil
}

func cloneSnapshotForMedia(snapshot SessionSnapshot) SessionSnapshot {
	clone := snapshot
	clone.History = CloneMessages(snapshot.History)
	clone.Scope = CloneScope(snapshot.Scope)
	if snapshot.Aliases != nil {
		clone.Aliases = make([]string, len(snapshot.Aliases))
		copy(clone.Aliases, snapshot.Aliases)
	}
	return clone
}

func countSessionSnapshotMedia(ctx context.Context, messages []providers.Message) (int, error) {
	count := 0
	add := func(locator string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if locator == "" {
			return nil
		}
		count++
		if count > media.MaxFrozenMediaOccurrences {
			return media.ErrFrozenMediaLimit
		}
		return nil
	}
	for _, message := range messages {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		for _, locator := range message.Media {
			if err := add(locator); err != nil {
				return 0, err
			}
		}
		for _, attachment := range message.Attachments {
			if err := add(attachment.Ref); err != nil {
				return 0, err
			}
			if err := add(attachment.URL); err != nil {
				return 0, err
			}
		}
		for _, part := range message.Parts {
			if err := add(part.URI); err != nil {
				return 0, err
			}
		}
	}
	return count, nil
}

func sessionSnapshotMediaSlots(messages []providers.Message, capacity int) []sessionMediaSlot {
	slots := make([]sessionMediaSlot, 0, capacity)
	for messageIndex := range messages {
		message := &messages[messageIndex]
		for mediaIndex := range message.Media {
			if message.Media[mediaIndex] != "" {
				slots = append(slots, sessionMediaSlot{
					locator: &message.Media[mediaIndex],
				})
			}
		}
		for attachmentIndex := range message.Attachments {
			attachment := &message.Attachments[attachmentIndex]
			hasURL := attachment.URL != ""
			if attachment.Ref != "" {
				slots = append(slots, sessionMediaSlot{
					locator:               &attachment.Ref,
					contentType:           &attachment.ContentType,
					filename:              &attachment.Filename,
					metadataAuthoritative: !hasURL,
				})
			}
			// URL is provider-effective when both fields are present. Process it
			// last so its canonical metadata also wins after materialization.
			if attachment.URL != "" {
				slots = append(slots, sessionMediaSlot{
					locator:               &attachment.URL,
					contentType:           &attachment.ContentType,
					filename:              &attachment.Filename,
					metadataAuthoritative: true,
				})
			}
		}
		for partIndex := range message.Parts {
			part := &message.Parts[partIndex]
			if part.URI != "" {
				slots = append(slots, sessionMediaSlot{
					locator:               &part.URI,
					contentType:           &part.MIMEType,
					filename:              &part.Filename,
					metadataAuthoritative: true,
				})
			}
		}
	}
	return slots
}

func validateMaterializedSessionMetadata(
	slots []sessionMediaSlot,
	values []media.Materialized,
) error {
	if len(slots) != len(values) {
		return media.ErrFrozenMediaTampered
	}
	for index, slot := range slots {
		if !slot.metadataAuthoritative {
			continue
		}
		if slot.contentType == nil || *slot.contentType != values[index].ContentType ||
			slot.filename == nil || *slot.filename != values[index].Filename {
			return media.ErrFrozenMediaTampered
		}
	}
	return nil
}

func applyFrozenSessionMedia(slots []sessionMediaSlot, values []media.FrozenReference) {
	for index, value := range values {
		*slots[index].locator = value.Ref
		if slots[index].contentType != nil {
			*slots[index].contentType = value.ContentType
		}
		if slots[index].filename != nil {
			*slots[index].filename = value.Filename
		}
	}
}

func applyMaterializedSessionMedia(slots []sessionMediaSlot, values []media.Materialized) {
	for index, value := range values {
		*slots[index].locator = value.URI
		if slots[index].contentType != nil {
			*slots[index].contentType = value.ContentType
		}
		if slots[index].filename != nil {
			*slots[index].filename = value.Filename
		}
	}
}
