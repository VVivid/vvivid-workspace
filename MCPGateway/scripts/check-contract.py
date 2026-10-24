"""Keep independently buildable backend implementations on the v1 contract."""
from pathlib import Path
root = Path(__file__).resolve().parents[2]
for name in ("token.go", "token_test.go"):
    expected = (root / "MCPGateway/trust" / name).read_text().replace("package trust", "package gatewaytrust", 1)
    for project in ("AgentShoppingList", "SSHMCP"):
        actual = (root / project / "internal/gatewaytrust" / name).read_text()
        if actual != expected:
            raise SystemExit(f"{project}: {name} differs from the v1 identity contract")
print("Gateway identity contract implementations match")
