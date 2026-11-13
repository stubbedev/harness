package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLSPLanguageIDs(t *testing.T) {
	t.Parallel()

	t.Run("normalizes patterns the way filetypes match", func(t *testing.T) {
		t.Parallel()

		cfg := loadYAML(t, `
lsp:
  terraformls:
    command: terraform-ls
    language_ids:
      tf: terraform
      .TFVars: terraform-vars
      tftest.hcl: terraform-test
`)
		require.Equal(t, LanguageIDs{
			"tf":         "terraform",
			"tfvars":     "terraform-vars",
			"tftest.hcl": "terraform-test",
		}, cfg.LSP["terraformls"].LanguageIDs)
	})

	for name, doc := range map[string]string{
		"rejects an empty pattern":              "lsp:\n  x:\n    language_ids:\n      \".\": terraform\n",
		"rejects an empty language ID":          "lsp:\n  x:\n    language_ids:\n      tf: \"\"\n",
		"rejects patterns that normalize alike": "lsp:\n  x:\n    language_ids:\n      tf: terraform\n      .TF: terraform-vars\n",
		"rejects a non-string language ID":      "lsp:\n  x:\n    language_ids:\n      tf: 3\n",
		"rejects a list":                        "lsp:\n  x:\n    language_ids: [tf]\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			jsonBytes, err := decodeConfig([]byte(doc))
			require.NoError(t, err)
			_, err = loadFromBytes([][]byte{jsonBytes})
			require.ErrorContains(t, err, "language_ids")
		})
	}
}
