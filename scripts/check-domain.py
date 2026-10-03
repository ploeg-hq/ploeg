from pathlib import Path
import subprocess
import sys
import tempfile

root = Path(__file__).resolve().parent.parent
with tempfile.TemporaryDirectory(prefix="ploeg-domain-") as temporary:
    subprocess.run([sys.executable, str(root / "scripts/generate-domain.py"), str(root / "docs/domain/model.yaml"), "-o", temporary], check=True)
    for generated in Path(temporary).glob("*.md"):
        if generated.read_bytes() != (root / "docs/domain" / generated.name).read_bytes():
            raise SystemExit(f"Stale domain page {generated.name}; run mise run domain")
