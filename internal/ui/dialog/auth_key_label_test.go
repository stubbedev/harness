package dialog

import (
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
	mcptools "github.com/stubbedev/harness/internal/agent/tools/mcp"
	"github.com/stubbedev/harness/internal/catalog"
	"github.com/stubbedev/harness/internal/oauth/copilot"
	"github.com/stubbedev/harness/internal/ui/common"
	"github.com/stubbedev/harness/internal/ui/keys"
)

func TestAuthDialogsRenderSelectKeyFromKeymap(t *testing.T) {
	com := common.DefaultCommon(nil)
	render := func() map[string]string {
		oauth, _ := NewOAuth(com, catalog.Provider{}, copilot.DeviceFlow{})
		oauth.State = OAuthStateDisplay
		oauth.width = 80

		mcp, _ := NewMCPAuth(com, []mcptools.PendingAuthServer{{Name: "srv", URL: "https://example.com"}}, nil)
		mcp.width = 80

		sso, _ := NewAWSSSO(com, "aws sso login")
		sso.SetURL("https://example.com/sso")
		sso.width = 80

		return map[string]string{
			"oauth":    ansi.Strip(oauth.innerDialogContent()),
			"mcp_auth": ansi.Strip(mcp.innerContent()),
			"aws_sso":  ansi.Strip(sso.innerDialogContent()),
		}
	}

	t.Cleanup(func() { keys.Install(nil) })

	keys.Install(nil)
	for name, view := range render() {
		require.Contains(t, view, "Press enter to", name)
	}

	keys.Install(map[string][]string{"dialog.select": {"ctrl+o"}})
	for name, view := range render() {
		require.Contains(t, view, "Press ctrl+o to", name)
		require.NotContains(t, view, "enter", name)
	}
}
