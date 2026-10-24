//go:build integration

package integration

import (
	"bytes"
	"os"
	"testing"

	"github.com/VVivid/vvivid-workspace/MCPGateway/internal/localdemo"
)

func TestLocalAgentThroughGatewayProcess(t *testing.T) {
	binary := os.Getenv("MCP_GATEWAY_BIN")
	if binary == "" {
		t.Fatal("MCP_GATEWAY_BIN is required; run make integration")
	}
	var output bytes.Buffer
	if err := localdemo.Run(t.Context(), binary, &output); err != nil {
		t.Fatalf("local acceptance scenario: %v\n%s", err, output.String())
	}
	t.Log(output.String())
}
