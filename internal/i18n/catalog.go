package i18n

// englishCatalog merges the per-area English message tables.
var englishCatalog = buildEnglish()

func buildEnglish() map[string]string {
	out := make(map[string]string, 600)
	tables := []map[string]string{
		englishCommands,
		englishFlags,
		englishOutput,
		englishRuntime,
		englishDoctor,
	}
	for _, table := range tables {
		for key, value := range table {
			out[key] = value
		}
	}
	return out
}

var catalogs = map[string]map[string]string{
	English: englishCatalog,
}
