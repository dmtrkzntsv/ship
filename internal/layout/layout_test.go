package layout

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseRef(t *testing.T) {
	r, err := ParseRef("winterflow/hub")
	require.NoError(t, err)
	assert.Equal(t, "hub", r.Name())
	assert.Equal(t, "winterflow-hub", r.Asset())
	assert.Equal(t, "winterflow", r.DefaultService())
	assert.Equal(t, "winterflow/hub", r.String())

	r, err = ParseRef("econumo")
	require.NoError(t, err)
	assert.Equal(t, "econumo", r.Name())
	assert.Equal(t, "econumo", r.Asset())

	for _, bad := range []string{"", "Econumo", "a/b/c", "/hub", "econumo/", "-x", "a b"} {
		_, err := ParseRef(bad)
		assert.Error(t, err, bad)
	}
}

func TestUnitName(t *testing.T) {
	cases := []struct {
		svc, name string
		existing  []string
		want      string
	}{
		{"econumo-beta", "econumo", nil, "econumo-beta"},
		{"twillingate", "twillingate-sql", []string{"twillingate"}, "twillingate-sql"},
		{"wf", "agent", []string{"wf"}, "wf-agent"},
		{"twillingate", "twillingate-sql", nil, "twillingate"},
	}
	for _, c := range cases {
		got, err := UnitName(c.svc, c.name, c.existing)
		require.NoError(t, err)
		assert.Equal(t, c.want, got)
	}
	_, err := UnitName("wf", "agent", []string{"wf", "wf-agent"})
	assert.Error(t, err)
}

func TestChannelsAndFiles(t *testing.T) {
	assert.True(t, IsMoving("latest"))
	assert.True(t, IsMoving("dev"))
	assert.False(t, IsMoving("v1.0.0"))
	assert.True(t, ValidName("a_b-1"))
	assert.False(t, ValidName("_a"))
	assert.Equal(t, "x-hub-linux-arm64", BinaryFile("x-hub", "arm64"))
	assert.Equal(t, "x-hub.service", UnitFile("x-hub"))
	assert.Equal(t, "x-hub.env.example", EnvExampleFile("x-hub"))
}

func TestValidateVersion(t *testing.T) {
	for _, ok := range []string{"v1.2.3", "v261024.3", "2026.10.24-1"} {
		assert.NoError(t, ValidateVersion(ok), ok)
	}
	for _, bad := range []string{"latest", "dev", "", "v1/2", "a b", ".", ".."} {
		assert.Error(t, ValidateVersion(bad), bad)
	}
}

func TestRenderUnit(t *testing.T) {
	p := Placeholders{Service: "tw", Bin: "/opt/tw/twillingate-sql", BinDir: "/opt/tw", EnvFile: "/etc/tw/twillingate-sql.env", DataDir: "/var/lib/tw", RunDir: "/run/tw", Unit: "tw-twillingate-sql"}
	out, err := RenderUnit("User={{USER}}\nExecStart={{BIN}}\nReadWritePaths={{RUN_DIR}}\n", p)
	require.NoError(t, err)
	assert.Equal(t, "User=tw\nExecStart=/opt/tw/twillingate-sql\nReadWritePaths=/run/tw\n", out)

	all := "{{SERVICE}} {{USER}} {{BIN}} {{BIN_DIR}} {{ENV_FILE}} {{DATA_DIR}} {{RUN_DIR}} {{UNIT}}"
	out, err = RenderUnit(all, p)
	require.NoError(t, err)
	assert.Equal(t, "tw tw /opt/tw/twillingate-sql /opt/tw /etc/tw/twillingate-sql.env /var/lib/tw /run/tw tw-twillingate-sql", out)

	_, err = RenderUnit("X={{PORT}}", p)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "{{PORT}}")
	assert.Error(t, CheckUnitTemplate("{{ BIN }}"))
	assert.NoError(t, CheckUnitTemplate("{{BIN}}"))
}

func TestRenderUnitComments(t *testing.T) {
	p := Placeholders{Service: "tw", DataDir: "/var/lib/tw"}
	// Comment lines may mention placeholders in prose: unknown ones are left
	// as written, known ones are filled in like anywhere else.
	tmpl := "# ship fills in the {{...}} placeholders\n  ; see {{FOO}}\n# data in {{DATA_DIR}}\nUser={{USER}}\n"
	out, err := RenderUnit(tmpl, p)
	require.NoError(t, err)
	assert.Equal(t, "# ship fills in the {{...}} placeholders\n  ; see {{FOO}}\n# data in /var/lib/tw\nUser=tw\n", out)
	assert.NoError(t, CheckUnitTemplate(tmpl))

	// a placeholder after a value is not a comment
	_, err = RenderUnit("Description=x # {{FOO}}\n", p)
	assert.Error(t, err)
}

func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for n, c := range files {
		require.NoError(t, os.WriteFile(filepath.Join(dir, n), []byte(c), 0o644))
	}
	return dir
}

func TestScanDir(t *testing.T) {
	dir := writeFiles(t, map[string]string{
		"winterflow-hub-linux-amd64":   "x",
		"winterflow-hub-linux-arm64":   "x",
		"winterflow-hub-linux-arm":     "x",
		"winterflow-hub.service":       "ExecStart={{BIN}}",
		"winterflow-hub.env.example":   "A=1",
		"winterflow-agent-linux-amd64": "x",
		"winterflow-agent-linux-arm64": "x",
		"winterflow-agent.service":     "User={{USER}}",
		"SHA256SUMS":                   "old",
	})
	assets, files, err := ScanDir("winterflow", dir)
	require.NoError(t, err)
	require.Len(t, assets, 2)
	assert.Equal(t, Asset{Name: "winterflow-agent", Arches: []string{"amd64", "arm64"}, HasUnit: true}, assets[0])
	assert.Equal(t, Asset{Name: "winterflow-hub", Arches: []string{"amd64", "arm64", "arm"}, HasUnit: true, HasEnvExample: true}, assets[1])
	assert.NotContains(t, files, "SHA256SUMS")
	assert.Len(t, files, 8)
	assert.IsIncreasing(t, files)
}

func TestScanDirTool(t *testing.T) {
	dir := writeFiles(t, map[string]string{"tool-linux-amd64": "x", "tool-linux-arm64": "x"})
	assets, _, err := ScanDir("tool", dir)
	require.NoError(t, err)
	require.Len(t, assets, 1)
	assert.False(t, assets[0].HasUnit)
}

func TestScanDirErrors(t *testing.T) {
	good := map[string]string{"winterflow-linux-amd64": "x", "winterflow-linux-arm64": "x"}
	with := func(extra map[string]string) map[string]string {
		m := map[string]string{}
		for k, v := range good {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	cases := map[string]map[string]string{
		"stray file":      with(map[string]string{"README.md": "x"}),
		"foreign asset":   with(map[string]string{"billing-linux-amd64": "x", "billing-linux-arm64": "x"}),
		"missing arm64":   {"winterflow-hub-linux-amd64": "x"},
		"bad placeholder": with(map[string]string{"winterflow.service": "X={{NOPE}}"}),
		"empty":           {},
		"only sums":       {"SHA256SUMS": "x"},
	}
	for name, files := range cases {
		_, _, err := ScanDir("winterflow", writeFiles(t, files))
		assert.Error(t, err, name)
	}
	_, _, err := ScanDir("winterflow", filepath.Join(t.TempDir(), "nope"))
	assert.Error(t, err)
}

func TestValidateChannel(t *testing.T) {
	for _, ok := range []string{"latest", "dev", "v1.2.3"} {
		assert.NoError(t, ValidateChannel(ok), ok)
	}
	for _, bad := range []string{"a b", "", ".", ".."} {
		assert.Error(t, ValidateChannel(bad), bad)
	}
}
