"""Build and validate the release website without third-party dependencies."""
import hashlib
import os
from pathlib import Path
import re
import shutil
from html.parser import HTMLParser

root = Path(__file__).resolve().parent.parent
version = os.environ.get("WEBSITE_VERSION", "development")
if version != "development" and not re.fullmatch(r"v\d+\.\d+\.\d+", version):
    raise SystemExit("WEBSITE_VERSION must be a stable vX.Y.Z tag")
out = root / "dist/website"
shutil.rmtree(out, ignore_errors=True)
shutil.copytree(root / "website", out)
page = out / "index.html"
# Fingerprint CSS so updated pages cannot reuse an older cached stylesheet.
stylesheet = out / "style.css"
css_name = f"style-{hashlib.sha256(stylesheet.read_bytes()).hexdigest()[:12]}.css"
stylesheet.rename(out / css_name)
for html in out.glob("*.html"):
    html.write_text(html.read_text().replace("@VERSION@", version).replace("/style.css", f"/{css_name}"))
shutil.copyfile(root / "scripts/install.sh", out / "install.sh")

class Links(HTMLParser):
    def handle_starttag(self, tag, attrs):
        for key, value in attrs:
            if key in ("href", "src") and value.startswith("/"):
                target = out / value.lstrip("/") if value != "/" else page
                assert target.is_file(), f"Missing asset: {value}"

for html in out.glob("*.html"):
    Links().feed(html.read_text())
assert (out / "install.sh").read_bytes() == (root / "scripts/install.sh").read_bytes()
assert version in page.read_text() and "@VERSION@" not in page.read_text()
assert (out / "404.html").is_file()
print(f"Website built and checked: {version}")
