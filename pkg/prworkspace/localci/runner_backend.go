package localci

func isProductionEvidenceStore(store EvidenceStore) bool {
	_, valid := store.(*FileEvidenceStore)
	return valid
}
