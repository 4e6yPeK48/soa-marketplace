package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	allure "github.com/allure-framework/allure-go/commons/gotest"
	"github.com/stretchr/testify/require"
)

func TestAllureContract(t *testing.T) {
	for _, mode := range []string{"pass", "fail"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			cmd := exec.Command(os.Args[0], "-test.run=^TestReportProbe$")
			cmd.Env = append(os.Environ(), "E2E_REPORT_PROBE="+mode, "ALLURE_RESULTS_DIR="+dir)
			output, err := cmd.CombinedOutput()
			if mode == "fail" {
				require.Error(t, err, "wrong expectation must fail Go")
				require.Contains(t, string(output), "controlled wrong expectation")
			} else {
				require.NoError(t, err, string(output))
			}
			files, err := filepath.Glob(filepath.Join(dir, "*-result.json"))
			require.NoError(t, err)
			require.Len(t, files, 1)
			data, err := os.ReadFile(files[0])
			require.NoError(t, err)
			var result M
			require.NoError(t, json.Unmarshal(data, &result))
			expected := "passed"
			if mode == "fail" {
				expected = "failed"
			}
			require.Equal(t, expected, result["status"])
			require.Contains(t, string(data), "Diagnostics")
			entries, err := os.ReadDir(dir)
			require.NoError(t, err)
			for _, entry := range entries {
				data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
				require.NoError(t, err)
				require.NotContains(t, string(data), "probe-password")
				require.NotContains(t, string(data), "probe-access-token")
			}
		})
	}
}

func TestReportProbe(t *testing.T) {
	mode := os.Getenv("E2E_REPORT_PROBE")
	if mode == "" {
		t.Skip("executed by TestAllureContract")
	}
	allure.Wrap(t, func(a *allure.Context) {
		defer a.Attachment("Diagnostics", []byte("saved before report finalization"), "text/plain")
		a.Step("Safe HTTP evidence", func(a *allure.Context) {
			a.Attachment("Response", sanitizeJSON([]byte("{\"password\":\"probe-password\",\"access_token\":\"probe-access-token\"}")), "application/json")
		})
		if strings.EqualFold(mode, "fail") {
			require.Equal(t, 200, 500, "controlled wrong expectation")
		}
	}, allure.WithTestCaseID("REPORT-PROBE"))
}
